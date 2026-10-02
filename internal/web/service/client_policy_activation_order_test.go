package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	handler "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestClientPolicyHotGrantCannotReviveRemovedBinding(t *testing.T) {
	for _, removal := range []string{"credential", "listener"} {
		t.Run(removal, func(t *testing.T) {
			svc, tunnel, owner, target := setupManagedActivationService(t)
			probe, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			ib := mkInbound(t, port, model.VLESS, `{"decryption":"none","clients":[]}`)
			cs := &ClientService{}
			if _, err := cs.Attach(&InboundService{}, owner.Id, []int{ib.Id}); err != nil {
				t.Fatal(err)
			}
			owner, err = cs.GetRecordByEmail(nil, owner.Email)
			if err != nil {
				t.Fatal(err)
			}
			disabled := owner.ToClient()
			disabled.Enable = false
			if _, err := cs.Update(&InboundService{}, owner.Id, *disabled, 0); err != nil {
				t.Fatal(err)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			process := currentXrayProcess()
			original := process.GetConfig()
			endpoint, err := process.GetAPIEndpoint()
			if err != nil {
				t.Fatal(err)
			}
			issuer, binding, window := managedActivationPrivateGrantIssuer(t, process, endpoint, owner.StableID)
			policyApplied, resume := make(chan struct{}), make(chan struct{})
			var mutationsMu sync.Mutex
			var mutations []string
			resumeOnce := func() {
				select {
				case <-resume:
				default:
					close(resume)
				}
			}
			defer resumeOnce()
			socket := managedActivationControlProxy(t, endpoint, func(ctx context.Context, method string) error {
				switch operation := filepath.Base(method); operation {
				case "AlterInbound", "CloseConnections", "RemoveInbound", "ApplyPolicies":
					mutationsMu.Lock()
					mutations = append(mutations, operation)
					mutationsMu.Unlock()
				}
				if strings.HasSuffix(method, "/ApplyPolicies") {
					close(policyApplied)
					select {
					case <-resume:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			})
			current := *original
			var private conf.APIConfig
			if err := json.Unmarshal(current.API, &private); err != nil {
				t.Fatal(err)
			}
			private.Listen = socket
			current.API, err = json.Marshal(private)
			if err != nil {
				t.Fatal(err)
			}
			process.SetConfig(&current)
			next := current
			next.InboundConfigs = slices.Clone(current.InboundConfigs)
			if removal == "credential" {
				for i := range next.InboundConfigs {
					if next.InboundConfigs[i].Tag == ib.Tag {
						next.InboundConfigs[i].Settings = []byte(`{"decryption":"none","clients":[]}`)
					}
				}
			} else {
				for i := range next.InboundConfigs {
					if next.InboundConfigs[i].Tag == tunnel.Tag {
						next.InboundConfigs = slices.Delete(next.InboundConfigs, i, i+1)
						break
					}
				}
			}
			var policy conf.ClientPolicyConfig
			if err := json.Unmarshal(current.ClientPolicy, &policy); err != nil {
				t.Fatal(err)
			}
			if len(policy.Policies) != 1 || policy.Policies[0].Enabled {
				t.Fatal("fixture did not begin with one disabled shared policy")
			}
			policy.Policies[0].Enabled = true
			policy.Policies[0].Version++
			next.ClientPolicy, err = json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				applied, err := panelruntime.NewLocal(panelruntime.LocalDeps{}).ApplyManagedConfig(ctx, process, &next, func(*command.Capabilities, *conf.ClientPolicyConfig) (*panelruntime.ManagedPolicyBootstrap, error) {
					return &panelruntime.ManagedPolicyBootstrap{}, nil
				})
				if err == nil && !applied {
					err = fmt.Errorf("candidate was not hot applied")
				}
				done <- err
			}()
			select {
			case <-policyApplied:
			case err := <-done:
				t.Fatalf("application ended before policy application: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			wantMutations := []string{"RemoveInbound", "ApplyPolicies"}
			if removal == "credential" {
				wantMutations = []string{"AlterInbound", "CloseConnections", "ApplyPolicies"}
			}
			mutationsMu.Lock()
			completedMutations := slices.Clone(mutations)
			mutationsMu.Unlock()
			if !slices.Equal(completedMutations, wantMutations) {
				t.Fatalf("binding removal did not precede policy application: %v", completedMutations)
			}
			// ApplyPolicies enables desired policy; execution also requires a finite
			// grant. Install it while that RPC's reply is still deliberately held.
			challenge, err := issuer.AuthorityChallenge(ctx)
			if err != nil {
				t.Fatal(err)
			}
			caps := issuer.Capabilities()
			grant := &command.ExecutionGrant{
				Authority: binding, InstanceId: caps.InstanceId, BootId: caps.BootId,
				ClientId: owner.StableID, WindowId: window, PolicyVersion: policy.Policies[0].Version,
				GrantId: "activation-order-grant", Sequence: 1, ChallengeId: challenge.ChallengeId,
				Capacity: 1024, Upload: &command.AuthorityShare{Rate: 4096, Burst: 64},
				Download: &command.AuthorityShare{Rate: 4096, Burst: 64}, LeaseDurationMillis: 5000,
			}
			installed, err := issuer.InstallAuthorityGrant(ctx, grant)
			if err != nil || installed.GetSealed() || !proto.Equal(installed.GetGrant(), grant) {
				t.Fatalf("finite execution grant was not installed: %v/%v", installed, err)
			}
			select {
			case err := <-done:
				t.Fatalf("application ended before releasing the policy reply: %v", err)
			default:
			}
			if removal == "credential" {
				if flow, err := dialManagedActivationVLESS(t, port, target, owner.UUID); err == nil {
					_ = flow.Close()
					t.Error("grant revived the credential pending removal")
				} else if os.IsTimeout(err) {
					t.Errorf("removed credential was not rejected: %v", err)
				}
				if err := managedActivationTunnelEcho(tunnel.Port); err != nil {
					t.Errorf("retained listener failed after grant: %v", err)
				}
			} else {
				if err := managedActivationTunnelEcho(tunnel.Port); err == nil {
					t.Error("grant revived the listener pending removal")
				} else if os.IsTimeout(err) {
					t.Errorf("removed listener remained available: %v", err)
				}
				if flow, err := dialManagedActivationVLESS(t, port, target, owner.UUID); err != nil {
					t.Errorf("retained credential failed after grant: %v", err)
				} else {
					_ = flow.Close()
				}
			}
			resumeOnce()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if process.GetConfig() != &next || !process.IsRunning() {
				t.Fatal("grant did not preserve the running process and install the candidate")
			}
		})
	}
}

// This runtime ordering fixture drives the core's private grant RPCs directly.
// Join the service-owned worker first: its disabled SQL policy intentionally
// differs from the enabled hot candidate being tested here.
func managedActivationPrivateGrantIssuer(t *testing.T, process *panelxray.Process, endpoint, clientID string) (*panelxray.ClientPolicyAPI, *command.AuthorityBinding, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	authority := managedAuthorityForProcess(process)
	if authority == nil {
		t.Fatal("fixture has no bound authority")
	}
	identity := authority.state.Journal.Identity()
	account, err := authority.state.Journal.Account(clientID)
	if err != nil {
		t.Fatal(err)
	}
	if err := stopManagedAuthority(ctx, process); err != nil {
		t.Fatal(err)
	}
	api, err := panelxray.DialClientPolicy(ctx, endpoint, authority.config.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	binding := &command.AuthorityBinding{AuthorityId: identity.AuthorityID, Generation: identity.Generation, NodeId: "local"}
	if err := api.BindAuthority(ctx, binding); err != nil {
		t.Fatal(err)
	}
	return api, binding, account.Policy.WindowID
}

func managedActivationTunnelEcho(port int) error {
	flow, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return err
	}
	defer flow.Close()
	_ = flow.SetDeadline(time.Now().Add(time.Second))
	if _, err := flow.Write([]byte("data")); err != nil {
		return err
	}
	reply := make([]byte, 4)
	_, err = io.ReadFull(flow, reply)
	if err == nil && string(reply) != "data" {
		return fmt.Errorf("unexpected echo: %q", reply)
	}
	return err
}

func managedActivationControlProxy(t *testing.T, endpoint string, after func(context.Context, string) error) string {
	t.Helper()
	conn, err := grpc.NewClient("unix://"+endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	dir, err := os.MkdirTemp("", "activation-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		name := strings.ReplaceAll(strings.TrimPrefix(info.FullMethod, "/"), "/", ".")
		descriptor, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(name))
		if err != nil {
			return nil, err
		}
		response := dynamicpb.NewMessage(descriptor.(protoreflect.MethodDescriptor).Output())
		if err := conn.Invoke(ctx, info.FullMethod, request, response); err != nil {
			return nil, err
		}
		return response, after(ctx, info.FullMethod)
	}))
	command.RegisterClientPolicyServiceServer(server, &command.UnimplementedClientPolicyServiceServer{})
	handler.RegisterHandlerServiceServer(server, &handler.UnimplementedHandlerServiceServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return socket
}
