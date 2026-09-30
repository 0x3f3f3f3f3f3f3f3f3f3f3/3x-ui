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

func TestPasswordProxyHotCapabilitiesBeforePreparation(t *testing.T) {
	for _, protocol := range []model.Protocol{model.Mixed, model.HTTP} {
		t.Run(string(protocol), func(t *testing.T) {
			svc, _, owner, _ := setupManagedActivationService(t)
			probe, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := probe.Addr().(*net.TCPAddr).Port
			_ = probe.Close()
			ib, _, err := (&InboundService{}).AddInbound(&model.Inbound{Enable: true, Protocol: protocol, Listen: "127.0.0.1", Port: port, Settings: passwordOwnerSettings(t, protocol, map[string]any{"user": "alice", "pass": "resource", "ownerClientId": owner.StableID})})
			if err != nil {
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
			missing := []string{"trusted-http-client-id-v1", "authenticated-credential-revocation-v1", "inbound-scoped-session-close-v1", ""}
			if protocol == model.Mixed {
				missing = append(missing, "trusted-socks-client-id-v1")
			}
			for _, operation := range []string{"remove-only", "rotate"} {
				for _, capability := range missing {
					t.Run(operation+"/missing-"+capability, func(t *testing.T) {
						dir, err := os.MkdirTemp("", "password-caps-")
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
						caps := proto.Clone(api.Capabilities()).(*command.Capabilities)
						caps.Capabilities = slices.DeleteFunc(caps.Capabilities, func(name string) bool { return name == capability })
						server := grpc.NewServer()
						command.RegisterClientPolicyServiceServer(server, &activationCapabilityServer{caps: caps})
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
						next := current
						next.InboundConfigs = slices.Clone(current.InboundConfigs)
						for i := range next.InboundConfigs {
							if next.InboundConfigs[i].Tag != ib.Tag {
								continue
							}
							var settings map[string]any
							if err := json.Unmarshal(next.InboundConfigs[i].Settings, &settings); err != nil {
								t.Fatal(err)
							}
							if operation == "remove-only" {
								settings["accounts"] = []any{}
							} else {
								settings["accounts"].([]any)[0].(map[string]any)["pass"] = "new-resource"
							}
							next.InboundConfigs[i].Settings, err = json.Marshal(settings)
							if err != nil {
								t.Fatal(err)
							}
						}
						process.SetConfig(&current)
						defer process.SetConfig(original)
						prepared := 0
						stop := errors.New("successful preflight reached preparation")
						applied, err := panelruntime.NewLocal(panelruntime.LocalDeps{}).ApplyManagedConfig(context.Background(), process, &next, func(*command.Capabilities, *conf.ClientPolicyConfig) (*panelruntime.ManagedPolicyBootstrap, error) {
							prepared++
							return nil, stop
						})
						if capability == "" {
							if applied || !errors.Is(err, stop) || prepared != 1 {
								t.Fatalf("capable core did not reach preparation: %t %d %v", applied, prepared, err)
							}
						} else if applied || !errors.Is(err, xray.ErrClientPolicyCapability) || prepared != 0 {
							t.Fatalf("missing capability reached preparation: %t %d %v", applied, prepared, err)
						}
						if !process.IsRunning() || process.GetConfig() != &current {
							t.Fatal("preflight rejection changed the running process")
						}
					})
				}
			}
		})
	}
}
