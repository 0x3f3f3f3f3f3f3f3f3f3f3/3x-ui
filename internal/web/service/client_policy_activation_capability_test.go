package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type activationCapabilityServer struct {
	command.UnimplementedClientPolicyServiceServer
	caps *command.Capabilities
}

func (s *activationCapabilityServer) GetCapabilities(context.Context, *command.Empty) (*command.Capabilities, error) {
	return s.caps, nil
}

func TestClientPolicyHotChangesNegotiateCapabilitiesBeforePreparation(t *testing.T) {
	for _, operation := range []string{"remove-user", "add-user", "add-inbound"} {
		t.Run(operation, func(t *testing.T) {
			checkClientPolicyHotCapabilities(t, operation)
		})
	}
}

func checkClientPolicyHotCapabilities(t *testing.T, operation string) {
	svc, _, owner, _ := setupManagedActivationService(t)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	ib := mkInbound(t, port, model.VLESS, `{"decryption":"none","clients":[]}`)
	if _, err := (&ClientService{}).Attach(&InboundService{}, owner.Id, []int{ib.Id}); err != nil {
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
	var state conf.ClientPolicyConfig
	if err := json.Unmarshal(original.ClientPolicy, &state); err != nil {
		t.Fatal(err)
	}
	api, err := xray.DialClientPolicy(context.Background(), endpoint, state.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	caps := api.Capabilities()
	missingCapabilities := []string{"inbound-scoped-session-close-v1", "authenticated-credential-revocation-v1", ""}
	if operation != "remove-user" {
		missingCapabilities = append(missingCapabilities, "trusted-vless-client-id-v1")
	}
	for _, missing := range missingCapabilities {
		t.Run("missing-"+missing, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "activation-caps-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "control.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(socket, 0o600); err != nil {
				_ = listener.Close()
				t.Fatal(err)
			}
			response := proto.Clone(caps).(*command.Capabilities)
			response.Capabilities = slices.DeleteFunc(response.Capabilities, func(name string) bool { return name == missing })
			server := grpc.NewServer()
			command.RegisterClientPolicyServiceServer(server, &activationCapabilityServer{caps: response})
			go func() { _ = server.Serve(listener) }()
			defer server.Stop()
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
			current.InboundConfigs = slices.Clone(original.InboundConfigs)
			next := current
			next.InboundConfigs = slices.Clone(current.InboundConfigs)
			for i := range next.InboundConfigs {
				if next.InboundConfigs[i].Tag == ib.Tag {
					switch operation {
					case "remove-user":
						next.InboundConfigs[i].Settings = []byte(`{"decryption":"none","clients":[]}`)
					case "add-user":
						current.InboundConfigs[i].Settings = []byte(`{"decryption":"none","clients":[]}`)
					}
				}
			}
			if operation == "add-inbound" {
				current.InboundConfigs = slices.DeleteFunc(current.InboundConfigs, func(inbound xray.InboundConfig) bool { return inbound.Tag == ib.Tag })
			}
			process.SetConfig(&current)
			defer process.SetConfig(original)
			runtime := panelruntime.NewLocal(panelruntime.LocalDeps{})
			prepared := 0
			stop := errors.New("preparation reached after successful negotiation")
			applied, err := runtime.ApplyManagedConfig(context.Background(), process, &next, func(*command.Capabilities, *conf.ClientPolicyConfig) (*panelruntime.ManagedPolicyBootstrap, error) {
				prepared++
				return nil, stop
			})
			if missing == "" {
				if applied || !errors.Is(err, stop) || prepared != 1 {
					t.Fatalf("capable core did not reach preparation: applied=%t prepared=%d err=%v", applied, prepared, err)
				}
			} else if applied || !errors.Is(err, xray.ErrClientPolicyCapability) || prepared != 0 {
				t.Fatalf("unsafe removal passed negotiation: missing=%s applied=%t prepared=%d err=%v", missing, applied, prepared, err)
			}
			if !process.IsRunning() || process.GetConfig() != &current {
				t.Fatal("preflight rejection changed the running process")
			}
		})
	}
}
