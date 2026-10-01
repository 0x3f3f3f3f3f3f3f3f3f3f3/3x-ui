package xray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
)

func TestMieruStartupNegotiatesBeforePreparation(t *testing.T) {
	for _, mode := range []struct {
		name, env  string
		compatible bool
	}{
		{"preceding-native-core", "XRAY_PRE_MIERU_MARKER_E2E_BINARY", false},
		{"current-core", "XRAY_E2E_BINARY", true},
	} {
		for _, transport := range []string{"TCP", "UDP"} {
			for _, empty := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/empty=%t", mode.name, transport, empty), func(t *testing.T) {
					binary := os.Getenv(mode.env)
					if binary == "" {
						t.Skip("set " + mode.env + " to matching native checkpoint")
					}
					dir, err := os.MkdirTemp("", "mieru-startup-")
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
					if err := clientpolicy.CreateStore(state, "mieru-startup"); err != nil {
						t.Fatal(err)
					}
					users := `[{"username":"wire-user","password":"wire-password","email":"label","clientId":"owner"}]`
					if empty {
						users = `[]`
					}
					port := freePort(t)
					raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService"]},"clientPolicy":{"stateFile":%q,"instanceId":"mieru-startup","policies":[{"clientId":"owner","version":1,"enabled":true,"multiplierMicros":1000000,"burstBytes":65536}]},"inbounds":[{"tag":"native","listen":"127.0.0.1","port":%d,"protocol":"mieru","settings":{"transport":%q,"users":%s}}],"outbounds":[{"protocol":"freedom"}]}`, socket, state, port, transport, users)
					var cfg Config
					if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
						t.Fatal(err)
					}
					process := NewTestProcess(&cfg, filepath.Join(dir, "bootstrap.json"))
					t.Cleanup(func() { _ = process.Stop() })
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					prepared := false
					stop := errors.New("negotiated before preparation")
					err = process.StartManaged(ctx, func(context.Context, *ClientPolicyAPI) error { prepared = true; return stop })
					if mode.compatible {
						if !prepared || !errors.Is(err, stop) {
							t.Fatalf("native core did not reach negotiated preparation: prepared=%t error=%v", prepared, err)
						}
					} else if prepared || !errors.Is(err, ErrClientPolicyCapability) || !strings.Contains(err.Error(), "trusted-mieru-client-id-v1") {
						t.Fatalf("preceding core crossed native capability gate: prepared=%t error=%v", prepared, err)
					}
					if process.IsRunning() || process.IsControlReady() {
						t.Fatal("rejected activation retained a usable child")
					}
					if transport == "TCP" {
						if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
							c.Close()
							t.Fatal("failed preparation opened business listener")
						}
					}
				})
			}
		}
	}
}
