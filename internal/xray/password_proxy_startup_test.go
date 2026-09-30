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

func TestPasswordProxyStartupNegotiatesBeforePreparation(t *testing.T) {
	for _, mode := range []struct {
		name, environment string
		compatible        bool
	}{{"older-core", "XRAY_PRE_PASSWORD_IDENTITY_E2E_BINARY", false}, {"current-core", "XRAY_E2E_BINARY", true}} {
		for _, protocol := range []string{"socks", "mixed", "http"} {
			for _, empty := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/empty=%t", mode.name, protocol, empty), func(t *testing.T) {
					binary := os.Getenv(mode.environment)
					if binary == "" {
						t.Skip("set " + mode.environment + " to the matching custom core")
					}
					dir, err := os.MkdirTemp("", "password-proxy-start-")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { os.RemoveAll(dir) })
					t.Setenv("XUI_BIN_FOLDER", dir)
					t.Setenv("XUI_LOG_FOLDER", dir)
					if err := os.Symlink(binary, filepath.Join(dir, GetBinaryName())); err != nil {
						t.Fatal(err)
					}
					state, socket := filepath.Join(dir, "state.db"), filepath.Join(dir, "control.sock")
					if err := clientpolicy.CreateStore(state, "password-startup"); err != nil {
						t.Fatal(err)
					}
					accounts := `[{"user":"wire-user","pass":"wire-password","email":"canonical-account","clientId":"owner"}]`
					if empty {
						accounts = `[]`
					}
					port := freePort(t)
					raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService"]},"clientPolicy":{"stateFile":%q,"instanceId":"password-startup","policies":[{"clientId":"owner","version":1,"enabled":true,"multiplierMicros":1000000,"burstBytes":65536}]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":%q,"settings":{"auth":"password","requireAuthentication":true,"accounts":%s}}],"outbounds":[{"protocol":"freedom"}]}`, socket, state, port, protocol, accounts)
					var config Config
					if err := json.Unmarshal([]byte(raw), &config); err != nil {
						t.Fatal(err)
					}
					process := NewTestProcess(&config, filepath.Join(dir, "bootstrap.json"))
					t.Cleanup(func() { process.Stop() })
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					prepared := false
					stop := errors.New("compatible core reached preparation")
					err = process.StartManaged(ctx, func(context.Context, *ClientPolicyAPI) error { prepared = true; return stop })
					if mode.compatible {
						if !prepared || !errors.Is(err, stop) {
							t.Fatalf("current core did not negotiate password identity: prepared=%t err=%v", prepared, err)
						}
					} else if prepared || !errors.Is(err, ErrClientPolicyCapability) || !strings.Contains(err.Error(), "client-id-v1") {
						t.Fatalf("older core crossed preparation: prepared=%t err=%v", prepared, err)
					}
					if process.IsRunning() || process.IsControlReady() {
						t.Fatal("failed activation left the child available")
					}
					if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
						conn.Close()
						t.Fatal("failed negotiation opened a business listener")
					}
				})
			}
		}
	}
}
