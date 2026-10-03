package sub

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/controller"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestNodeDelegationHTTPActualOwnedCore(t *testing.T) {
	for _, mode := range []string{"delegated-restart", "activated-local-refuses"} {
		t.Run(mode, func(t *testing.T) {
			h := newNativeHTTPHarness(t, "node_delegation_http_"+mode)
			t.Logf("node delegation backend: %s", database.GetDB().Dialector.Name())
			tunnel := h.add(t, "tunnel", "delegated-tunnel", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, h.target.Addr().(*net.TCPAddr).Port))
			h.api(t, http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: model.Client{Email: "delegated-owner", SubID: "delegated-sub", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}, InboundIds: []int{tunnel.Id}})
			if err := database.GetDB().Create(&model.ApiToken{Name: "delegation-wire", Token: crypto.HashTokenSHA256("delegation-wire-token"), Scope: model.ApiScopeNodeSync, Enabled: true}).Error; err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("delegation-wire-synthetic-session-key"))))
			controller.NewNodeAuthorityAPIController(router.Group(""))
			srv := httptest.NewTLSServer(router)
			defer srv.Close()
			address, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(address.Port())
			if err != nil {
				t.Fatal(err)
			}
			pin := sha256.Sum256(srv.Certificate().Raw)
			remote := panelruntime.NewRemote(&model.Node{Id: 1, Name: "delegated-owned", Scheme: "https", Address: address.Hostname(), Port: port, BasePath: "/", ApiToken: "delegation-wire-token", Enable: true, AllowPrivateAddress: true, TlsVerifyMode: "pin", PinnedCertSha256: base64.StdEncoding.EncodeToString(pin[:])}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			request := panelruntime.NodeDelegationRequest{AuthorityID: "coordinator-authority", Generation: 3, NodeID: "delegated-node-a"}
			if mode == "activated-local-refuses" {
				if err := h.svc.RestartXray(true); err != nil {
					t.Fatal(err)
				}
				if result, err := remote.ConfigureDelegation(ctx, request); err == nil || result != nil {
					t.Fatal("actual TLS setup converted an activated local source")
				}
				local, err := remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
				if err != nil || local.ExecutionRole == nil || local.ExecutionRole.Mode != panelruntime.NodeExecutionLocal {
					t.Fatal("refused setup changed the local owner")
				}
				flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer flow.Close()
				sshHTTPEcho(t, flow, "local-after-refusal")
				return
			}
			for _, tc := range []struct {
				name, body, token string
				status            int
			}{
				{"invalid-token", `{"authorityId":"coordinator-authority","generation":3,"nodeId":"delegated-node-a"}`, "invalid-wire-token", 401},
				{"duplicate-binding", `{"authorityId":"other","authorityId":"coordinator-authority","generation":3,"nodeId":"delegated-node-a"}`, "delegation-wire-token", 400},
				{"null-role-field", `{"authorityId":"coordinator-authority","generation":null,"nodeId":"delegated-node-a"}`, "delegation-wire-token", 400},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/panel/api/server/clientPolicyDelegation", strings.NewReader(tc.body))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Authorization", "Bearer "+tc.token)
					resp, err := srv.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					_ = resp.Body.Close()
					if resp.StatusCode != tc.status {
						t.Fatalf("actual TLS refusal status=%d want=%d", resp.StatusCode, tc.status)
					}
				})
			}
			configured, err := remote.ConfigureDelegation(ctx, request)
			if err != nil || configured == nil || configured.Role != request.Role() {
				t.Fatalf("real TLS fresh delegation failed: %+v/%v", configured, err)
			}
			if result, err := remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{}); err == nil || result != nil {
				t.Fatal("fresh setup alone claimed a running core")
			}
			if err := h.svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			first, err := remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
			if err != nil || first == nil || first.ExecutionRole == nil || *first.ExecutionRole != request.Role() || first.Capabilities.InstanceId != configured.InstanceID {
				t.Fatalf("configured role differs from actual running core: %+v/%v", first, err)
			}
			assertHeld := func() {
				t.Helper()
				flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer flow.Close()
				if err := flow.SetDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				_, _ = flow.Write([]byte("no-coordinator-grant"))
				if n, err := flow.Read(make([]byte, 32)); n != 0 || err == nil {
					t.Fatalf("actual delegated Tunnel forwarded%d bytes without grant", n)
				}
			}
			assertHeld()
			if retry, err := remote.ConfigureDelegation(ctx, request); err != nil || retry == nil || *retry != *configured {
				t.Fatalf("live actual TLS configuration retry changed role/source: %+v/%v", retry, err)
			}
			changed := request
			changed.Generation++
			if result, err := remote.ConfigureDelegation(ctx, changed); err == nil || result != nil {
				t.Fatal("live actual TLS setup changed immutable coordinator generation")
			}
			bound := panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: first.Capabilities.InstanceId, ExpectedBootID: first.Capabilities.BootId}
			if err := h.svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			if result, err := remote.DiscoverAuthority(ctx, bound); err == nil || result != nil {
				t.Fatal("actual TLS delegated restart accepted predecessor boot")
			}
			fresh, err := remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
			if err != nil || fresh == nil || fresh.ExecutionRole == nil || *fresh.ExecutionRole != request.Role() || fresh.Capabilities.InstanceId != configured.InstanceID || fresh.Capabilities.BootId == bound.ExpectedBootID {
				t.Fatalf("actual TLS delegated restart lost role/source/fresh boot: %+v/%v", fresh, err)
			}
			assertHeld()
			endpoint, err := h.svc.GetXrayAPIEndpoint()
			if err != nil {
				t.Fatal(err)
			}
			actual, err := xray.DialClientPolicy(ctx, endpoint, configured.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			defer actual.Close()
			if actual.Capabilities().BootId != fresh.Capabilities.BootId {
				t.Fatal("actual TLS identity differs from current private core")
			}
			var client model.ClientRecord
			if err := database.GetDB().First(&client, "email = ?", "delegated-owner").Error; err != nil {
				t.Fatal(err)
			}
			state, err := actual.GetClient(ctx, client.StableID)
			if err != nil || state == nil || state.Usage == nil || state.Usage.RawUpload != 0 || state.Usage.RawDownload != 0 || state.Usage.BilledBytes != 0 {
				t.Fatalf("no-grant delegated demand acquired business usage: %+v/%v", state, err)
			}
		})
	}
}
