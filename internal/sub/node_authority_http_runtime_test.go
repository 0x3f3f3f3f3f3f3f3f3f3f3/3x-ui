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

func TestNodeAuthorityDiscoveryHTTPActualOwnedCore(t *testing.T) {
	h := newNativeHTTPHarness(t, "node_authority_http")
	t.Logf("node authority discovery backend: %s", database.GetDB().Dialector.Name())
	tunnel := h.add(t, "tunnel", "authority-discovery", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, h.target.Addr().(*net.TCPAddr).Port))
	h.api(t, http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: model.Client{Email: "authority-owner", SubID: "authority-sub", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}, InboundIds: []int{tunnel.Id}})
	if err := database.GetDB().Create(&model.ApiToken{Name: "authority-wire", Token: crypto.HashTokenSHA256("authority-wire-token"), Scope: model.ApiScopeNodeSync, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("authority-wire-synthetic-session-key"))))
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
	remote := panelruntime.NewRemote(&model.Node{Id: 1, Name: "authority-owned", Scheme: "https", Address: address.Hostname(), Port: port, BasePath: "/", ApiToken: "authority-wire-token", Enable: true, AllowPrivateAddress: true, TlsVerifyMode: "pin", PinnedCertSha256: base64.StdEncoding.EncodeToString(pin[:])}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	first, err := remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := h.svc.GetXrayAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := xray.DialClientPolicy(ctx, endpoint, first.Capabilities.InstanceId)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Capabilities().BootId != first.Capabilities.BootId {
		t.Fatal("HTTP authority discovery differs from the actual private core")
	}
	if err := actual.Close(); err != nil {
		t.Fatal(err)
	}
	request := panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: first.Capabilities.InstanceId, ExpectedBootID: first.Capabilities.BootId}
	bound, err := remote.DiscoverAuthority(ctx, request)
	if err != nil || bound.Challenge.ChallengeId == first.Challenge.ChallengeId {
		t.Fatal("real authenticated bound discovery did not issue a fresh challenge")
	}
	invalid, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/panel/api/server/clientPolicyAuthority", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	invalid.Header.Set("Authorization", "Bearer invalid-authority-wire-token")
	rejected, err := srv.Client().Do(invalid)
	if err != nil {
		t.Fatal(err)
	}
	_ = rejected.Body.Close()
	if rejected.StatusCode != http.StatusUnauthorized {
		t.Fatal("actual TLS route accepted an invalid bearer token")
	}
	echo := func(payload string) {
		t.Helper()
		flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		sshHTTPEcho(t, flow, payload)
		if err := flow.Close(); err != nil {
			t.Fatal(err)
		}
	}
	echo("one!")
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	if stale, err := remote.DiscoverAuthority(ctx, request); err == nil || stale != nil {
		t.Fatal("actual TLS route accepted predecessor boot after real restart")
	}
	fresh, err := remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || fresh.Capabilities.BootId == request.ExpectedBootID || fresh.Capabilities.InstanceId != request.ExpectedInstanceID {
		t.Fatal("actual HTTP discovery did not report the replacement core")
	}
	echo("two!")
	endpoint, err = h.svc.GetXrayAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	actual, err = xray.DialClientPolicy(ctx, endpoint, fresh.Capabilities.InstanceId)
	if err != nil {
		t.Fatal(err)
	}
	defer actual.Close()
	if actual.Capabilities().BootId != fresh.Capabilities.BootId {
		t.Fatal("replacement HTTP identity differs from private core")
	}
	if err := actual.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	var owner model.ClientRecord
	if err := database.GetDB().First(&owner, "email = ?", "authority-owner").Error; err != nil {
		t.Fatal(err)
	}
	state, err := actual.GetClient(ctx, owner.StableID)
	if err != nil || state.Usage.RawUpload != 8 || state.Usage.RawDownload != 8 || state.Usage.BilledBytes != 32 {
		t.Fatal("HTTP discovery/restart changed literal Tunnel payload billing")
	}
}
