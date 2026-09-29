package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
)

func TestManagedMutationRequiresCredentialRevocationBeforeHandlerChanges(t *testing.T) {
	for _, protocol := range []string{"vless", "vmess", "trojan", "shadowsocks"} {
		for _, missing := range []string{"authenticated-credential-revocation-v1", "inbound-scoped-session-close-v1", ""} {
			t.Run(protocol+"/"+missing, func(t *testing.T) {
				caps := &command.Capabilities{ApiVersion: 1, InstanceId: "node", Epoch: 1, Capabilities: []string{
					"trusted-tunnel-client-id-v1", "trusted-vless-client-id-v1", "trusted-vmess-client-id-v1", "trusted-trojan-client-id-v1", "trusted-shadowsocks-aead-client-id-v1",
					"shared-directional-rate-v1", "fixed-point-billing-v1", "live-session-control-v1", "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1",
					"authenticated-credential-revocation-v1", "inbound-scoped-session-close-v1",
				}}
				caps.Capabilities = slices.DeleteFunc(caps.Capabilities, func(name string) bool { return name == missing })
				api, probe := managedMutationAPI(t, caps, false)
				user := map[string]any{"email": "managed", "id": "936997e1-3b0c-4de9-9eea-047ee5829d3e", "clientId": "owner", "password": "test-pass", "cipher": "aes-128-gcm", "method": "aes-128-gcm", "security": "auto"}
				if err := api.AddUser(protocol, "owned", user); missing == "" && err != nil {
					t.Errorf("capable core rejected user: %v", err)
				} else if missing != "" && !errors.Is(err, ErrClientPolicyCapability) {
					t.Errorf("user accepted a core without %s: %v", missing, err)
				}
				settings := map[string]any{"clients": []any{user}, "decryption": "none", "method": "aes-128-gcm", "network": "tcp,udp"}
				inbound, err := json.Marshal(map[string]any{"tag": "owned", "listen": "127.0.0.1", "port": 12345, "protocol": protocol, "settings": settings})
				if err != nil {
					t.Fatal(err)
				}
				if err := api.AddInbound(inbound); missing == "" && err != nil {
					t.Errorf("capable core rejected inbound: %v", err)
				} else if missing != "" && !errors.Is(err, ErrClientPolicyCapability) {
					t.Errorf("inbound accepted a core without %s: %v", missing, err)
				}
				if missing != "" && probe.calls.Load() != 0 {
					t.Fatal("missing revocation capability reached a handler mutation")
				}
				if missing == "" {
					if probe.calls.Load() < 2 {
						t.Fatal("compatible handlers were not updated")
					}
					got := <-probe.users
					if got.ClientId != "owner" || got.Email != "managed" {
						t.Fatalf("authenticated identity changed: %+v", got)
					}
				}
			})
		}
	}
}

func TestManagedProcessNegotiatesCredentialRevocationBeforePreparation(t *testing.T) {
	for _, mode := range []struct {
		name, environment string
		compatible        bool
	}{{"older-core", "XRAY_PRE_REVOCATION_E2E_BINARY", false}, {"current-core", "XRAY_E2E_BINARY", true}} {
		t.Run(mode.name, func(t *testing.T) {
			binary := os.Getenv(mode.environment)
			if binary == "" {
				t.Skip("set " + mode.environment + " to the matching custom core")
			}
			dir, err := os.MkdirTemp("", "policy-auth-start-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			t.Setenv("XUI_BIN_FOLDER", dir)
			t.Setenv("XUI_LOG_FOLDER", dir)
			if err := os.Symlink(binary, filepath.Join(dir, GetBinaryName())); err != nil {
				t.Fatal(err)
			}
			state, socket := filepath.Join(dir, "state.db"), filepath.Join(dir, "control.sock")
			if err := clientpolicy.CreateStore(state, "auth-startup"); err != nil {
				t.Fatal(err)
			}
			port := freePort(t)
			raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService"]},"clientPolicy":{"stateFile":%q,"instanceId":"auth-startup","policies":[{"clientId":"owner","version":1,"enabled":true,"multiplierMicros":1000000,"burstBytes":65536}]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"vless","settings":{"decryption":"none","clients":[{"id":"936997e1-3b0c-4de9-9eea-047ee5829d3e","email":"managed","clientId":"owner"}]}}],"outbounds":[{"protocol":"freedom"}]}`, socket, state, port)
			var config Config
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			process := NewTestProcess(&config, filepath.Join(dir, "bootstrap.json"))
			t.Cleanup(func() { _ = process.Stop() })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			prepared := false
			stop := errors.New("compatible core reached preparation")
			err = process.StartManaged(ctx, func(context.Context, *ClientPolicyAPI) error {
				prepared = true
				return stop
			})
			if mode.compatible {
				if !prepared || !errors.Is(err, stop) {
					t.Fatalf("compatible core rejected before preparation: %v", err)
				}
			} else if prepared || !errors.Is(err, ErrClientPolicyCapability) {
				t.Fatalf("older core crossed the preparation boundary: prepared=%t err=%v", prepared, err)
			}
			if process.IsRunning() || process.IsControlReady() {
				t.Fatal("failed negotiation/preparation left the child available")
			}
			if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
				_ = conn.Close()
				t.Fatal("failed negotiation opened a business listener")
			}
		})
	}
}
