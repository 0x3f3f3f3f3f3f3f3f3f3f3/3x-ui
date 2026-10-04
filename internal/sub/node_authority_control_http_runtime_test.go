package sub

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/crypto/nodetoken"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/controller"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"google.golang.org/protobuf/proto"
)

func TestNodeAuthorityControlHTTPActualJournalGrant(t *testing.T) {
	t.Run("independent-journal", testNodeAuthorityControlIndependentJournal)
	t.Run("actual-local-issuer-refuses", testNodeAuthorityControlActualLocalIssuer)
}

type nodeControlHTTPPeer struct {
	remote                           *panelruntime.Remote
	server                           *httptest.Server
	loseInstall, lostSuccess, outage atomic.Bool
	loseEnroll, lostEnrollSuccess    atomic.Bool
}

func newNodeControlHTTPPeer(t *testing.T) *nodeControlHTTPPeer {
	t.Helper()
	if err := database.GetDB().Create(&model.ApiToken{Name: "grant-wire", Token: crypto.HashTokenSHA256("grant-wire-synthetic-token"), Scope: model.ApiScopeNodeSync, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	codec, err := nodetoken.NewCodec(nodetoken.ModeRequired, &nodetoken.Keyring{ActiveID: "test-key", Keys: map[string][32]byte{"test-key": {1, 2, 3}}})
	if err != nil {
		t.Fatal(err)
	}
	nodetoken.Init(codec)
	t.Cleanup(func() { nodetoken.Init(nil) })
	stored, err := nodetoken.Encrypt(1, "grant-wire-synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("grant-wire-synthetic-session-key"))))
	controller.NewNodeAuthorityAPIController(router.Group(""))
	peer := &nodeControlHTTPPeer{}
	peer.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if peer.outage.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		lostInstall := r.URL.Path == "/panel/api/server/clientPolicyAuthority/install" && peer.loseInstall.CompareAndSwap(true, false)
		lostEnroll := r.URL.Path == "/panel/api/server/clientPolicyAuthority/enroll" && peer.loseEnroll.CompareAndSwap(true, false)
		if lostInstall || lostEnroll {
			reply := httptest.NewRecorder()
			router.ServeHTTP(reply, r)
			var envelope struct {
				Success bool `json:"success"`
			}
			succeeded := reply.Code == 200 && json.Unmarshal(reply.Body.Bytes(), &envelope) == nil && envelope.Success
			if lostInstall {
				peer.lostSuccess.Store(succeeded)
			} else {
				peer.lostEnrollSuccess.Store(succeeded)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(peer.server.Close)
	address, err := url.Parse(peer.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(peer.server.Certificate().Raw)
	peer.remote = panelruntime.NewRemote(&model.Node{Id: 1, Name: "grant-owned", Scheme: "https", Address: address.Hostname(), Port: port, BasePath: "/", ApiToken: stored, Enable: true, AllowPrivateAddress: true, TlsVerifyMode: "pin", PinnedCertSha256: base64.StdEncoding.EncodeToString(pin[:])}, nil)
	return peer
}

func nodeControlHTTPClient(t *testing.T, h *sshHTTPHarness) (model.Inbound, model.ClientRecord) {
	t.Helper()
	tunnel := h.add(t, "tunnel", "journal-tunnel", fmt.Sprintf(`{"rewriteAddress":"127.0.0.1","rewritePort":%d,"allowedNetwork":"tcp,udp","clients":[]}`, h.target.Addr().(*net.TCPAddr).Port))
	h.api(t, http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: model.Client{Email: "journal-owner", SubID: "journal-sub", Enable: true, TotalGB: 10000, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}, InboundIds: []int{tunnel.Id}})
	var client model.ClientRecord
	if err := database.GetDB().Where("email = ?", "journal-owner").First(&client).Error; err != nil {
		t.Fatal(err)
	}
	return tunnel, client
}

func nodeControlHTTPNoBytes(t *testing.T, tunnel model.Inbound) {
	t.Helper()
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	_ = flow.SetDeadline(time.Now().Add(150 * time.Millisecond))
	_, _ = flow.Write([]byte("ungranted"))
	n, err := flow.Read(make([]byte, 32))
	if n != 0 || err == nil {
		t.Fatalf("ungranted Tunnel forwarded %d bytes", n)
	}
}

func nodeControlHTTPGrant(issued policyauthority.Grant) *command.ExecutionGrant {
	r, b := issued.Request, issued.Request.Binding
	return &command.ExecutionGrant{Authority: &command.AuthorityBinding{AuthorityId: b.Identity.AuthorityID, Generation: b.Identity.Generation, NodeId: b.NodeBoot.NodeID}, InstanceId: b.NodeBoot.SourceID, BootId: b.NodeBoot.BootID, ClientId: b.ClientID, WindowId: b.WindowID, PolicyVersion: b.PolicyVersion, GrantId: issued.GrantID, Sequence: issued.Sequence, ChallengeId: r.ChallengeID, Capacity: r.Capacity, Upload: &command.AuthorityShare{Unlimited: r.Upload.Unlimited, Rate: r.Upload.Rate, Burst: r.Upload.Burst}, Download: &command.AuthorityShare{Unlimited: r.Download.Unlimited, Rate: r.Download.Rate, Burst: r.Download.Burst}, LeaseDurationMillis: uint64(r.LeaseDuration.Milliseconds())}
}

func testNodeAuthorityControlIndependentJournal(t *testing.T) {
	h := newNativeHTTPHarness(t, "node_control_http")
	t.Logf("node authority journal HTTP backend: %s", database.GetDB().Dialector.Name())
	tunnel, client := nodeControlHTTPClient(t, h)
	journalDir := t.TempDir()
	info, err := os.Lstat(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("coordinator fixture initial directory mode: %o", info.Mode().Perm())
	if err := os.Chmod(journalDir, 0700); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(journalDir, "coordinator.db")
	journal, id, err := policyauthority.Create(journalPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	peer := newNodeControlHTTPPeer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	setup := panelruntime.NodeDelegationRequest{AuthorityID: id.AuthorityID, Generation: id.Generation, NodeID: "journal-node-a"}
	if _, err := peer.remote.ConfigureDelegation(ctx, setup); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	discovery, err := peer.remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	bound := panelruntime.AuthorityDiscoveryRequest{ExpectedInstanceID: discovery.Capabilities.InstanceId, ExpectedBootID: discovery.Capabilities.BootId}
	adapter, err := panelruntime.NewRemoteAuthorityAPI(ctx, peer.remote, bound, setup.Role())
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := h.svc.GetXrayAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := xray.DialClientPolicy(ctx, endpoint, bound.ExpectedInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer actual.Close()
	state, err := actual.GetClient(ctx, client.StableID)
	if err != nil || state == nil || state.Policy == nil || state.Usage == nil || !proto.Equal(state.Usage, &command.Usage{}) || state.Policy.MultiplierMicros != 2000000 {
		t.Fatalf("actual canonical zero seed unavailable: %v", err)
	}
	if state.Policy.QuotaBytes != 10000 || state.Policy.Version == 0 {
		t.Fatal("actual policy differs from HTTP-created client")
	}
	var resets int64
	if err := database.GetDB().Model(&model.ClientPolicyReset{}).Where("client_id = ?", client.StableID).Count(&resets).Error; err != nil || resets != 0 {
		t.Fatal("fixture is not the canonical initial window")
	}
	policy := policyauthority.Policy{WindowID: "initial:" + client.StableID, Version: state.Policy.Version, QuotaBytes: 10000, Upload: policyauthority.Direction{Unlimited: true}, Download: policyauthority.Direction{Unlimited: true}}
	if err := journal.AddAccount(policyauthority.Seed{ClientID: client.StableID, Policy: policy}); err != nil {
		t.Fatal(err)
	}
	boot := policyauthority.NodeBoot{NodeID: setup.NodeID, SourceID: bound.ExpectedInstanceID, BootID: bound.ExpectedBootID}
	if err := journal.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	binding := &command.AuthorityBinding{AuthorityId: id.AuthorityID, Generation: id.Generation, NodeId: setup.NodeID}
	if err := adapter.BindAuthority(ctx, binding); err != nil {
		t.Fatal(err)
	}
	nodeControlHTTPNoBytes(t, tunnel)
	demand, err := adapter.ReadAuthorityRequests(ctx, binding, 128)
	if err != nil || demand == nil || len(demand.Requests) != 1 || demand.Requests[0].ClientId != client.StableID || demand.Requests[0].PolicyVersion != state.Policy.Version {
		t.Fatalf("actual grant demand missing: %v", err)
	}
	// The refused stream may still be waiting for its two-second core demand
	// timeout after the client closes. Drain it before installing a grant, so
	// its probe bytes cannot become later granted traffic and pollute literals.
	time.Sleep(2100 * time.Millisecond)
	before, err := actual.GetClient(ctx, client.StableID)
	if err != nil || !proto.Equal(before.Usage, &command.Usage{}) {
		t.Fatal("refused no-grant probe reached the target or accounting")
	}
	challenge, err := adapter.AuthorityChallenge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := policyauthority.Request{Binding: policyauthority.Binding{Identity: id, NodeBoot: boot, ClientID: client.StableID, WindowID: policy.WindowID, PolicyVersion: policy.Version}, RequestID: demand.Requests[0].RequestId, ChallengeID: challenge.ChallengeId, Capacity: 512, Upload: policy.Upload, Download: policy.Download, LeaseDuration: 1500 * time.Millisecond}
	issued, err := journal.Issue(request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := journal.Issue(request)
	if err != nil || retry.GrantID != issued.GrantID {
		t.Fatal("allocation retry duplicated the independent grant")
	}
	grant := nodeControlHTTPGrant(issued)
	peer.loseInstall.Store(true)
	if reply, err := adapter.InstallAuthorityGrant(ctx, grant); err == nil || reply != nil || !peer.lostSuccess.Load() {
		t.Fatal("lost successful mutation was acknowledged or not actually executed")
	}
	account, err := journal.Account(client.StableID)
	if err != nil || account.HeldCapacity != 512 || account.Usage != (policyauthority.Usage{}) {
		t.Fatal("lost reply released or charged unreported allocation")
	}
	installed, err := adapter.GetAuthorityGrant(ctx, client.StableID, grant.GrantId)
	if err != nil || installed.Sealed || !proto.Equal(installed.Grant, grant) {
		t.Fatalf("lost installation cannot be recovered by exact get: %v", err)
	}
	if _, err := adapter.InstallAuthorityGrant(ctx, grant); err != nil {
		t.Fatalf("identical install retry: %v", err)
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	sshHTTPEcho(t, flow, "tcp12345")
	udpTarget, err := net.ListenPacket("udp4", h.target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udpTarget.Close()
	go func() {
		buffer := make([]byte, 64)
		for {
			n, peer, err := udpTarget.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = udpTarget.WriteTo(buffer[:n], peer)
		}
	}()
	udp, err := net.DialTimeout("udp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	sshHTTPEcho(t, udp, "udp12345")
	used, err := adapter.GetAuthorityGrant(ctx, client.StableID, grant.GrantId)
	if err != nil || used.Usage.RawUpload != 16 || used.Usage.RawDownload != 16 || used.Usage.BilledBytes != 64 || used.Usage.Remainder != 0 {
		t.Fatalf("literal TCP/UDP 2x accounting: %+v / %v", used, err)
	}
	report := policyauthority.Report{Binding: request.Binding, GrantID: grant.GrantId, Sequence: used.Sequence, Usage: policyauthority.Usage{RawUpload: 16, RawDownload: 16, BilledBytes: 64}}
	if err := journal.Report(report); err != nil {
		t.Fatal(err)
	}
	if err := journal.Report(report); err != nil {
		t.Fatal(err)
	}
	account, err = journal.Account(client.StableID)
	if err != nil || account.HeldCapacity != 448 || account.Usage != (policyauthority.Usage{RawUpload: 16, RawDownload: 16, BilledBytes: 64}) {
		t.Fatal("duplicate cumulative report duplicated billing or capacity")
	}
	challenge, err = adapter.AuthorityChallenge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	renewal := &command.AuthorityRenewalRequest{ExpectedBootId: boot.BootID, ClientId: client.StableID, GrantId: grant.GrantId, ChallengeId: challenge.ChallengeId, Sequence: 1, LeaseDurationMillis: 2000}
	renewedAt := time.Now()
	if err := adapter.RenewAuthorityGrant(ctx, renewal); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	if err := adapter.RenewAuthorityGrant(ctx, renewal); err != nil {
		t.Fatalf("exact renewal replay refused: %v", err)
	}
	peer.outage.Store(true)
	if reply, err := adapter.GetAuthorityGrant(ctx, client.StableID, grant.GrantId); err == nil || reply != nil {
		t.Fatal("HTTP outage acknowledged grant state")
	}
	account, err = journal.Account(client.StableID)
	if err != nil || account.HeldCapacity != 448 {
		t.Fatal("outage released unsealed capacity")
	}
	peer.outage.Store(false)
	if remaining := time.Until(renewedAt.Add(2150 * time.Millisecond)); remaining > 0 {
		time.Sleep(remaining)
	}
	nodeControlHTTPNoBytes(t, tunnel)
	expired, err := adapter.GetAuthorityGrant(ctx, client.StableID, grant.GrantId)
	if err != nil || expired.Usage.RawUpload != 16 || expired.Usage.RawDownload != 16 || expired.Usage.BilledBytes != 64 {
		t.Fatal("expiry replay extended lease or forwarded ungranted bytes")
	}
	paused, err := adapter.PauseAuthorityGrant(ctx, client.StableID, grant.GrantId)
	if err != nil || !paused.Sealed {
		t.Fatalf("pause acknowledgement: %v", err)
	}
	sealed, err := adapter.SealAuthorityGrant(ctx, client.StableID, grant.GrantId)
	if err != nil || !sealed.Sealed || sealed.Usage.BilledBytes != 64 {
		t.Fatalf("seal acknowledgement: %v", err)
	}
	report.Sequence, report.Seal = sealed.Sequence, true
	if err := journal.Report(report); err != nil {
		t.Fatal(err)
	}
	if err := journal.Report(report); err != nil {
		t.Fatal(err)
	}
	account, err = journal.Account(client.StableID)
	if err != nil || account.HeldCapacity != 0 || account.WindowUsed != 64 || account.Usage.BilledBytes != 64 {
		t.Fatal("sealed duplicate report failed conservation")
	}
	// Hold another allocation across a real restart; losing a boot never releases
	// the independent journal's unreported remaining capacity.
	challenge, err = adapter.AuthorityChallenge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nextRequest := request
	nextRequest.RequestID = "restart-allocation"
	nextRequest.ChallengeID = challenge.ChallengeId
	nextRequest.Capacity = 256
	next, err := journal.Issue(nextRequest)
	if err != nil {
		t.Fatal(err)
	}
	nextGrant := nodeControlHTTPGrant(next)
	if _, err := adapter.InstallAuthorityGrant(ctx, nextGrant); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	fresh, err := peer.remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || fresh.Capabilities.InstanceId != boot.SourceID || fresh.Capabilities.BootId == boot.BootID || fresh.ExecutionRole == nil || *fresh.ExecutionRole != setup.Role() {
		t.Fatalf("actual restart binding: %v", err)
	}
	for _, op := range []struct {
		name string
		call func() (bool, error)
	}{
		{"requests", func() (bool, error) { r, e := adapter.ReadAuthorityRequests(ctx, binding, 128); return r != nil, e }},
		{"install", func() (bool, error) { r, e := adapter.InstallAuthorityGrant(ctx, nextGrant); return r != nil, e }},
		{"get", func() (bool, error) {
			r, e := adapter.GetAuthorityGrant(ctx, client.StableID, nextGrant.GrantId)
			return r != nil, e
		}},
		{"pause", func() (bool, error) {
			r, e := adapter.PauseAuthorityGrant(ctx, client.StableID, nextGrant.GrantId)
			return r != nil, e
		}},
		{"seal", func() (bool, error) {
			r, e := adapter.SealAuthorityGrant(ctx, client.StableID, nextGrant.GrantId)
			return r != nil, e
		}},
		{"renew", func() (bool, error) { e := adapter.RenewAuthorityGrant(ctx, renewal); return e == nil, e }},
	} {
		t.Run("stale-boot/"+op.name, func(t *testing.T) {
			hasReply, e := op.call()
			if e == nil || hasReply {
				t.Fatal("old boot operation accepted")
			}
		})
	}
	nodeControlHTTPNoBytes(t, tunnel)
	account, err = journal.Account(client.StableID)
	if err != nil || account.HeldCapacity != 256 || account.WindowUsed != 64 || account.Usage.BilledBytes != 64 {
		t.Fatal("restart reused uncertain capacity or duplicated historical billing")
	}
	// Verify the same conservative balance survives independent journal reopen.
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := policyauthority.Open(journalPath, id)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	account, err = reopened.Account(client.StableID)
	if err != nil || account.HeldCapacity != 256 || account.Usage.BilledBytes != 64 {
		t.Fatal("independent reopen lost allocation or reports")
	}
}

func testNodeAuthorityControlActualLocalIssuer(t *testing.T) {
	h := newNativeHTTPHarness(t, "node_control_local_http")
	t.Logf("node authority local issuer HTTP backend: %s", database.GetDB().Dialector.Name())
	tunnel, client := nodeControlHTTPClient(t, h)
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	peer := newNodeControlHTTPPeer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	found, err := peer.remote.DiscoverAuthority(ctx, panelruntime.AuthorityDiscoveryRequest{})
	if err != nil || found.ExecutionRole == nil || found.ExecutionRole.Mode != panelruntime.NodeExecutionLocal {
		t.Fatal("local fixture not owned")
	}
	raw, err := os.ReadFile(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority", "authority.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Identity policyauthority.Identity `json:"identity"`
		SourceID string                   `json:"sourceId"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.SourceID != found.Capabilities.InstanceId || manifest.Identity.AuthorityID == "" || manifest.Identity.Generation == 0 {
		t.Fatal("cannot prove actual local issuer identity")
	}
	endpoint, err := h.svc.GetXrayAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	api, err := xray.DialClientPolicy(ctx, endpoint, found.Capabilities.InstanceId)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	policy, err := api.GetClient(ctx, client.StableID)
	if err != nil {
		t.Fatal(err)
	}
	binding := panelruntime.NodeAuthorityControlBinding{ExpectedInstanceID: found.Capabilities.InstanceId, ExpectedBootID: found.Capabilities.BootId, AuthorityID: manifest.Identity.AuthorityID, Generation: manifest.Identity.Generation, NodeID: "local"}
	grant := &command.ExecutionGrant{Authority: &command.AuthorityBinding{AuthorityId: binding.AuthorityID, Generation: binding.Generation, NodeId: binding.NodeID}, InstanceId: binding.ExpectedInstanceID, BootId: binding.ExpectedBootID, ClientId: client.StableID, WindowId: "initial:" + client.StableID, PolicyVersion: policy.Policy.Version, GrantId: "valid-local-grant", Sequence: 1, ChallengeId: found.Challenge.ChallengeId, Capacity: 256, Upload: &command.AuthorityShare{Unlimited: true}, Download: &command.AuthorityShare{Unlimited: true}, LeaseDurationMillis: 1000}
	if err := api.BindAuthority(ctx, grant.Authority); err != nil {
		t.Fatalf("local tuple is not the core actual existing binding: %v", err)
	}
	renewal := &command.AuthorityRenewalRequest{ExpectedBootId: binding.ExpectedBootID, ClientId: client.StableID, GrantId: grant.GrantId, ChallengeId: found.Challenge.ChallengeId, Sequence: 1, LeaseDurationMillis: 1000}
	installRequest := panelruntime.NodeAuthorityInstallRequest{Binding: binding, Grant: grant}
	renewRequest := panelruntime.NodeAuthorityRenewalRequest{Binding: binding, Renewal: renewal}
	if installRequest.Validate() != nil || renewRequest.Validate() != nil {
		t.Fatal("local refusal uses incomplete or invalid payload")
	}
	request := panelruntime.NodeAuthorityGrantRequest{Binding: binding, ClientID: client.StableID, GrantID: grant.GrantId}
	for _, op := range []struct {
		name string
		call func() (bool, error)
	}{
		{"requests", func() (bool, error) {
			r, e := peer.remote.ReadAuthorityRequests(ctx, panelruntime.NodeAuthorityRequestsRequest{Binding: binding, Limit: 128})
			return r != nil, e
		}},
		{"install", func() (bool, error) {
			r, e := peer.remote.InstallAuthorityGrant(ctx, installRequest)
			return r != nil, e
		}},
		{"get", func() (bool, error) { r, e := peer.remote.GetAuthorityGrant(ctx, request); return r != nil, e }},
		{"pause", func() (bool, error) { r, e := peer.remote.PauseAuthorityGrant(ctx, request); return r != nil, e }},
		{"seal", func() (bool, error) { r, e := peer.remote.SealAuthorityGrant(ctx, request); return r != nil, e }},
		{"renew", func() (bool, error) { r, e := peer.remote.RenewAuthorityGrant(ctx, renewRequest); return r != nil, e }},
	} {
		t.Run(op.name, func(t *testing.T) {
			hasReply, e := op.call()
			if e == nil || hasReply {
				t.Fatal("actual local tuple admitted remote authority operation")
			}
		})
	}
	if installed, err := api.GetAuthorityGrant(ctx, client.StableID, grant.GrantId); err == nil || installed != nil {
		t.Fatal("refused local HTTP install mutated the private core")
	}
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	sshHTTPEcho(t, flow, "local-after-refusal")
}
