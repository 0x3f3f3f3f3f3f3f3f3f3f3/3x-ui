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
	"testing"
	"time"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	handler "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
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
			granted, resume := make(chan struct{}), make(chan struct{})
			resumeOnce := func() {
				select {
				case <-resume:
				default:
					close(resume)
				}
			}
			defer resumeOnce()
			socket := managedActivationControlProxy(t, endpoint, func(ctx context.Context, method string) error {
				if strings.HasSuffix(method, "/ApplyPolicies") {
					close(granted)
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
			case <-granted:
			case err := <-done:
				t.Fatalf("application ended before policy grant: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
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
