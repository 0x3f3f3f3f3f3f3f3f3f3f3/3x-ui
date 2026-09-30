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
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"

	command "github.com/xtls/xray-core/app/clientpolicy/command"
)

func TestTunnelFixedOutboundRequiresCapabilityBeforeHandlerMutation(t *testing.T) {
	for _, owner := range []string{"", "stable-owner"} {
		for _, supported := range []bool{false, true} {
			t.Run(fmt.Sprintf("owner=%s/supported=%t", owner, supported), func(t *testing.T) {
				caps := &command.Capabilities{ApiVersion: 1, InstanceId: "node", Epoch: 1, Capabilities: []string{"trusted-tunnel-client-id-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "live-session-control-v1", "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1"}}
				if supported {
					caps.Capabilities = append(caps.Capabilities, "tunnel-fixed-outbound-v1")
				}
				api, probe := managedMutationAPI(t, caps, false)
				raw := []byte(fmt.Sprintf(`{"tag":"acl","listen":"127.0.0.1","port":24101,"protocol":"tunnel","settings":{"allowedNetwork":"tcp","rewriteAddress":"127.0.0.1","rewritePort":9001,"clientId":%q,"outboundTag":"selected"}}`, owner))
				required, err := ManagedHotDiffCapabilities(&HotDiff{AddedInbounds: [][]byte{raw}})
				if err != nil || !slices.Contains(required, "tunnel-fixed-outbound-v1") {
					t.Fatalf("hot preflight omitted fixed outbound: %v %v", required, err)
				}
				err = api.AddInbound(raw)
				if supported {
					if err != nil || probe.calls.Load() != 1 {
						t.Fatalf("capable core rejected fixed outbound: %v calls=%d", err, probe.calls.Load())
					}
				} else if !errors.Is(err, ErrClientPolicyCapability) || probe.calls.Load() != 0 {
					t.Fatalf("old core mutated without fixed outbound enforcement: %v calls=%d", err, probe.calls.Load())
				}
			})
		}
	}
}

func TestTunnelFixedOutboundStartupNegotiatesBeforePreparation(t *testing.T) {
	for _, mode := range []struct {
		name, environment string
		compatible        bool
	}{
		{"older-core", "XRAY_PRE_FIXED_OUTBOUND_E2E_BINARY", false},
		{"current-core", "XRAY_E2E_BINARY", true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			binary := os.Getenv(mode.environment)
			if binary == "" {
				t.Skip("set " + mode.environment + " to the matching custom core")
			}
			dir, err := os.MkdirTemp("", "policy-acl-start-")
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
			if err := clientpolicy.CreateStore(state, "acl-startup"); err != nil {
				t.Fatal(err)
			}
			port := freePort(t)
			raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService"]},"clientPolicy":{"stateFile":%q,"instanceId":"acl-startup","policies":[{"clientId":"owner","version":1,"enabled":true,"multiplierMicros":1000000,"burstBytes":65536}]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"allowedNetwork":"tcp,udp","rewriteAddress":"127.0.0.1","rewritePort":9001,"clientId":"owner","outboundTag":"selected"}}],"outbounds":[{"protocol":"freedom"}]}`, socket, state, port)
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
			err = process.StartManaged(ctx, func(context.Context, *ClientPolicyAPI) error { prepared = true; return stop })
			if mode.compatible {
				if !prepared || !errors.Is(err, stop) {
					t.Fatalf("current core did not negotiate fixed outbound: prepared=%t err=%v", prepared, err)
				}
			} else if prepared || !errors.Is(err, ErrClientPolicyCapability) || !strings.Contains(err.Error(), "tunnel-fixed-outbound-v1") {
				t.Fatalf("older core crossed preparation boundary: prepared=%t err=%v", prepared, err)
			}
			if process.IsRunning() || process.IsControlReady() {
				t.Fatal("failed activation left core available")
			}
			if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
				_ = conn.Close()
				t.Fatal("failed negotiation opened business listener")
			}
		})
	}
}
