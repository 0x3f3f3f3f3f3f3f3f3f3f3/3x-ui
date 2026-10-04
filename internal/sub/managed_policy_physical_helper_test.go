package sub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
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
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/controller"
	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/robfig/cron/v3"
)

type managedPhysicalWebContext struct{ cron *cron.Cron }

func (s *managedPhysicalWebContext) GetCron() *cron.Cron     { return s.cron }
func (s *managedPhysicalWebContext) GetCtx() context.Context { return context.Background() }
func (s *managedPhysicalWebContext) GetWSHub() any           { return nil }

type managedProductHTTPPeer struct {
	server    *httptest.Server
	token     string
	partition atomic.Bool
}

func managedFixtureSecret(t *testing.T) string {
	t.Helper()
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(random[:])
}

func newManagedProductHTTPPeer(t *testing.T) *managedProductHTTPPeer {
	t.Helper()
	return newManagedProductHTTPPeerWithToken(t, managedFixtureSecret(t))
}

func newManagedProductHTTPPeerWithToken(t *testing.T, token string) *managedProductHTTPPeer {
	t.Helper()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	codec, err := nodetoken.NewCodec(nodetoken.ModeRequired, &nodetoken.Keyring{ActiveID: "physical-fixture", Keys: map[string][32]byte{"physical-fixture": key}})
	if err != nil {
		t.Fatal(err)
	}
	previous := nodetoken.Active()
	nodetoken.Init(codec)
	t.Cleanup(func() { nodetoken.Init(previous) })
	if err := database.GetDB().Create(&model.ApiToken{Name: "managed-physical-admin", Token: crypto.HashTokenSHA256(token), Scope: model.ApiScopeAdmin, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	peer := &managedProductHTTPPeer{token: token}
	router := gin.New()
	router.Use(managedPhysicalPartitionMiddleware(peer))
	router.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte(managedFixtureSecret(t)))))
	previousServer := global.GetWebServer()
	scheduler := cron.New()
	global.SetWebServer(&managedPhysicalWebContext{cron: scheduler})
	t.Cleanup(func() {
		scheduler.Stop()
		global.SetWebServer(previousServer)
	})
	controller.NewAPIController(router.Group(""))
	server := httptest.NewTLSServer(router)
	t.Cleanup(server.Close)
	peer.server = server
	return peer
}

func managedProductRPC[Result any](t *testing.T, peer *managedProductHTTPPeer, method, suffix string, payload any) Result {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, peer.server.URL+"/panel/api/server/clientPolicyCoordinator"+suffix, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+peer.token)
	response, err := peer.server.Client().Do(req)
	if err != nil {
		t.Fatal("protected product RPC failed", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Success bool   `json:"success"`
		Obj     Result `json:"obj"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, panelruntime.NodeAuthorityMessageLimit+1)).Decode(&envelope); err != nil || response.StatusCode != 200 || !envelope.Success {
		t.Fatalf("protected product RPC %s failed status=%d decode=%v", suffix, response.StatusCode, err)
	}
	return envelope.Obj
}

type managedPhysicalConfig struct {
	Coordinator                             service.ManagedPolicyCoordinatorStatus
	NodeID, Multiplier, ManifestPath, Token string
	Quota                                   int64
	UploadRate, DownloadRate                int64
	Native                                  bool
	SSHPublicKey                            string
}

type managedPhysicalManifest struct {
	PID, TunnelPort, TargetPort                                           int
	URL, Pin, NodeID, SourceID, BootID, LocalClientID, LocalPolicyVersion string
	RuntimeDir, SQLStorage, CoreSHA256                                    string
	NativeExports                                                         map[string]string
	Backend                                                               string
}

type managedPhysicalNode struct {
	manifest        managedPhysicalManifest
	token           string
	process         *exec.Cmd
	input           io.WriteCloser
	done            chan struct{}
	waitErr         error
	manifestPath    string
	controlSequence uint64
}

func startManagedPhysicalNode(t *testing.T, coordinator service.ManagedPolicyCoordinatorStatus, nodeID, multiplier string, quota int64) *managedPhysicalNode {
	return startManagedPhysicalNodeProfile(t, coordinator, nodeID, multiplier, quota, 0, 0)
}

func startManagedPhysicalNodeProfile(t *testing.T, coordinator service.ManagedPolicyCoordinatorStatus, nodeID, multiplier string, quota, upload, download int64) *managedPhysicalNode {
	t.Helper()
	dir := t.TempDir()
	cfg := managedPhysicalConfig{Coordinator: coordinator, NodeID: nodeID, Multiplier: multiplier, Quota: quota, UploadRate: upload, DownloadRate: download, Token: managedFixtureSecret(t), ManifestPath: filepath.Join(dir, "ready.json")}
	cfg.Native = os.Getenv("XUI_MANAGED_PHYSICAL_NATIVE") == "1"
	cfg.SSHPublicKey = os.Getenv("XUI_MANAGED_PHYSICAL_SSH_PUBLIC_KEY")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "private-fixture.json")
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(dir, "node-process.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestManagedPolicyTwoPhysicalNodesActualTunnelBilling$", "-test.count=1", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), "XUI_MANAGED_PHYSICAL_HELPER_CONFIG="+configPath)
	cmd.Stdout, cmd.Stderr = log, log
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	node := &managedPhysicalNode{token: cfg.Token, process: cmd, input: input, done: make(chan struct{}), manifestPath: cfg.ManifestPath}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { node.waitErr = cmd.Wait(); close(node.done) }()
	t.Cleanup(func() {
		_, _ = io.WriteString(input, "quit\n")
		_ = input.Close()
		select {
		case <-node.done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-node.done
		}
		_ = log.Close()
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-node.done:
			t.Fatalf("physical node exited before ready: %v (private fixture log retained for this test)", node.waitErr)
		default:
		}
		raw, err := os.ReadFile(cfg.ManifestPath)
		if err == nil {
			if err := json.Unmarshal(raw, &node.manifest); err != nil {
				t.Fatal("malformed physical node receipt", err)
			}
			if node.manifest.PID != cmd.Process.Pid || node.manifest.NodeID != nodeID || node.manifest.LocalPolicyVersion != "1" {
				t.Fatal("physical node receipt does not match process")
			}
			binary, err := os.ReadFile(os.Getenv("XRAY_E2E_BINARY"))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(binary)
			if node.manifest.CoreSHA256 != hex.EncodeToString(digest[:]) || node.manifest.Backend != database.GetDB().Dialector.Name() {
				t.Fatal("physical node binary or SQL backend does not match parent")
			}
			t.Logf("physical node %s backend: %s coreSHA256: %s", nodeID, node.manifest.Backend, node.manifest.CoreSHA256)
			return node
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("physical node did not become ready")
	return nil
}

func registerManagedPhysicalInventory(t *testing.T, node *managedPhysicalNode, parentID string) service.ManagedPolicyEnrollmentRequest {
	t.Helper()
	address, err := url.Parse(node.manifest.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	inventory := model.Node{Name: node.manifest.NodeID, Scheme: "https", Address: address.Hostname(), Port: port, BasePath: "/", Enable: true, AllowPrivateAddress: true, TlsVerifyMode: "pin", PinnedCertSha256: node.manifest.Pin}
	if err := database.GetDB().Create(&inventory).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := nodetoken.Encrypt(inventory.Id, node.token)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&inventory).Update("api_token", stored).Error; err != nil {
		t.Fatal(err)
	}
	return service.ManagedPolicyEnrollmentRequest{InventoryID: inventory.Id, ParentClientID: parentID, NodeID: node.manifest.NodeID, SourceID: node.manifest.SourceID, LocalClientID: node.manifest.LocalClientID, LocalPolicyVersion: node.manifest.LocalPolicyVersion}
}

func runManagedPhysicalNode(t *testing.T, configPath string) {
	t.Helper()
	info, err := os.Stat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("physical fixture credentials require private0600 file")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg managedPhysicalConfig
	if json.Unmarshal(raw, &cfg) != nil || !cfg.Coordinator.Active || cfg.Token == "" {
		t.Fatal("invalid private physical fixture")
	}
	h := newNativeHTTPHarness(t, "managed_physical_"+cfg.NodeID)
	managedPhysicalEchoTargets(t, h)
	tunnel := h.add(t, "tunnel", "physical-tunnel", fmt.Sprintf(`{"rewriteAddress":"127.0.0.1","rewritePort":%d,"allowedNetwork":"tcp,udp","clients":[]}`, h.target.Addr().(*net.TCPAddr).Port))
	bindings := []int{tunnel.Id}
	if cfg.Native {
		mieru := h.add(t, "mieru", "physical-mieru", `{"transport":"TCP","mtu":1400,"clients":[]}`)
		ssh := h.add(t, "ssh", "physical-ssh", `{"allowPassword":false,"clients":[]}`)
		bindings = append(bindings, mieru.Id, ssh.Id)
	}
	h.api(t, "POST", "/panel/api/clients/add", service.ClientCreatePayload{Client: model.Client{Email: "physical-local-owner", SubID: "physical-local-sub", Enable: true, TotalGB: cfg.Quota, SSHAuthorizedKeys: cfg.SSHPublicKey, Policy: &model.ClientPolicyOptions{Multiplier: cfg.Multiplier, UploadBytesPerSecond: cfg.UploadRate, DownloadBytesPerSecond: cfg.DownloadRate}}, InboundIds: bindings})
	var local model.ClientRecord
	if err := database.GetDB().First(&local, "email = ?", "physical-local-owner").Error; err != nil {
		t.Fatal(err)
	}
	if cfg.Native {
		h.api(t, "POST", "/panel/api/inbounds/add", map[string]any{"protocol": "snell", "tag": "physical-snell", "listen": "127.0.0.1", "port": sshHTTPPort(t), "enable": true, "ownerClientId": local.StableID, "settings": `{"version":6,"clients":[]}`, "streamSettings": "{}"})
	}
	peer := newManagedProductHTTPPeerWithToken(t, cfg.Token)
	generation, err := strconv.ParseUint(cfg.Coordinator.Generation, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	node := &service.ClientPolicyNodeService{}
	if _, err := node.ConfigureDelegation(context.Background(), panelruntime.NodeDelegationRequest{AuthorityID: cfg.Coordinator.AuthorityID, Generation: generation, NodeID: cfg.NodeID}); err != nil {
		t.Fatal("actual stopped node delegation failed", err)
	}
	if err := h.svc.RestartXray(true); err != nil {
		t.Fatal("actual delegated core startup failed", err)
	}
	discovery, err := node.DiscoverAuthority(context.Background(), panelruntime.AuthorityDiscoveryRequest{})
	if err != nil {
		t.Fatal("actual owned discovery failed", err)
	}
	if err := database.GetDB().First(&local, "stable_id = ?", local.StableID).Error; err != nil {
		t.Fatal(err)
	}
	endpoint, err := h.svc.GetXrayAPIEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := xray.DialClientPolicy(context.Background(), endpoint, discovery.Capabilities.InstanceId)
	if err != nil {
		t.Fatal(err)
	}
	state, stateErr := actual.GetClient(context.Background(), local.StableID)
	_ = actual.Close()
	if stateErr != nil || state == nil || state.Policy == nil || state.Policy.Version != 1 || local.DesiredPolicyVersion != 1 {
		t.Fatal("actual local version1 was not prepared", stateErr)
	}
	pin := sha256.Sum256(peer.server.Certificate().Raw)
	file, err := os.Open(os.Getenv("XRAY_E2E_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	_ = file.Close()
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	storage := ""
	if database.GetDB().Dialector.Name() == "postgres" {
		if err := database.GetDB().Raw("SELECT current_schema()").Scan(&storage).Error; err != nil {
			t.Fatal(err)
		}
	} else {
		var rows []struct{ Name, File string }
		if err := database.GetDB().Raw("PRAGMA database_list").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Name == "main" {
				storage = row.File
			}
		}
	}
	if storage == "" {
		t.Fatal("physical node has no independent SQL storage")
	}
	if _, err := os.Stat(filepath.Join(config.GetDBFolderPath(), "client-policy", "authority", "journal.db")); err != nil {
		t.Fatal("actual original node journal absent", err)
	}
	manifest := managedPhysicalManifest{PID: os.Getpid(), TunnelPort: tunnel.Port, URL: peer.server.URL, Pin: base64.StdEncoding.EncodeToString(pin[:]), NodeID: cfg.NodeID, SourceID: discovery.Capabilities.InstanceId, BootID: discovery.Capabilities.BootId, LocalClientID: local.StableID, LocalPolicyVersion: strconv.FormatInt(local.DesiredPolicyVersion, 10), RuntimeDir: config.GetDBFolderPath(), SQLStorage: storage, CoreSHA256: hex.EncodeToString(hash.Sum(nil))}
	manifest.TargetPort = h.target.Addr().(*net.TCPAddr).Port
	manifest.Backend = database.GetDB().Dialector.Name()
	if cfg.Native {
		manifest.NativeExports = managedPhysicalExportNative(t, h, local.SubID, filepath.Dir(cfg.ManifestPath))
	}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ManifestPath+".tmp", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cfg.ManifestPath+".tmp", cfg.ManifestPath); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	var sequence uint64
	for scanner.Scan() {
		if scanner.Text() == "quit" {
			return
		}
		sequence = runManagedPhysicalControl(t, scanner.Text(), sequence, cfg.ManifestPath, h, node, peer, &manifest)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func managedPhysicalEchoTargets(t *testing.T, h *sshHTTPHarness) {
	t.Helper()
	address := h.target.Addr().String()
	packet, err := net.ListenPacket("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packet.Close() })
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, peer, err := packet.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = packet.WriteTo(buffer[:n], peer)
		}
	}()
	_ = h.target.Close()
	target, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	h.target = target
	t.Cleanup(func() { _ = target.Close() })
	go func() {
		for {
			flow, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer flow.Close()
				var first [1]byte
				if _, err := io.ReadFull(flow, first[:]); err != nil || first[0] == '!' {
					return
				}
				if first[0] == 'U' {
					_, _ = io.Copy(io.Discard, flow)
					return
				}
				if first[0] == 'D' {
					_, _ = flow.Write(bytes.Repeat([]byte("d"), 65536))
					return
				}
				if _, err := flow.Write(first[:]); err != nil {
					return
				}
				_, _ = io.Copy(flow, flow)
			}()
		}
	}()
}

func managedPhysicalExchange(t *testing.T, port int, network string, payload []byte, echo bool) {
	t.Helper()
	flow, err := net.DialTimeout(network, fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Error("actual physical Tunnel connection failed", err)
		return
	}
	defer flow.Close()
	_ = flow.SetDeadline(time.Now().Add(5 * time.Second))
	if n, err := flow.Write(payload); err != nil || n != len(payload) {
		t.Errorf("actual physical Tunnel write failed n=%d: %v", n, err)
		return
	}
	buffer := make([]byte, len(payload))
	if echo {
		if _, err := io.ReadFull(flow, buffer); err != nil || !bytes.Equal(buffer, payload) {
			t.Errorf("actual physical Tunnel echo failed: %v", err)
		}
	} else if n, err := flow.Read(buffer); n != 0 || err != io.EOF {
		t.Errorf("one-way actual target did not close without output: n=%d err=%v", n, err)
	}
}

type managedPhysicalCase struct {
	product *managedProductHTTPPeer
	parent  model.ClientRecord
	nodes   []*managedPhysicalNode
}

func newManagedPhysicalCase(t *testing.T, scope model.ClientPolicyScope, multiplier string, quota, upload, download int64) *managedPhysicalCase {
	t.Helper()
	if os.Getenv("XRAY_E2E_BINARY") == "" {
		t.Fatal("actual paired core required")
	}
	cleanup, err := testpg.IsolatePackage("managed_physical_" + t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	seedSubDB(t)
	product := newManagedProductHTTPPeer(t)
	status := managedProductRPC[service.ManagedPolicyCoordinatorStatus](t, product, "POST", "/activate", map[string]any{})
	nodes := []*managedPhysicalNode{
		startManagedPhysicalNodeProfile(t, status, "physical-node-a", multiplier, quota, upload, download),
		startManagedPhysicalNodeProfile(t, status, "physical-node-b", multiplier, quota, upload, download),
	}
	t.Cleanup(func() { _ = service.StopManagedPolicyCoordinator(context.Background()) })
	if nodes[0].manifest.PID == nodes[1].manifest.PID || nodes[0].manifest.PID == os.Getpid() || nodes[1].manifest.PID == os.Getpid() || nodes[0].manifest.SourceID == nodes[1].manifest.SourceID || nodes[0].manifest.RuntimeDir == nodes[1].manifest.RuntimeDir || nodes[0].manifest.SQLStorage == nodes[1].manifest.SQLStorage {
		t.Fatal("physical nodes share process or persistent state")
	}
	parent := model.ClientRecord{Email: "physical-parent", Enable: true, TotalGB: quota, Policy: &model.ClientPolicyOptions{Scope: &scope, Multiplier: multiplier, UploadBytesPerSecond: upload, DownloadBytesPerSecond: download}}
	if err := database.GetDB().Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	for _, value := range []int64{quota, quota + 1, quota + 2, quota + 3, quota + 4, quota + 5, quota} {
		if err := database.GetDB().Table("clients").Where("stable_id = ?", parent.StableID).Update("total_gb", value).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := service.PrepareClientPolicies([]string{parent.StableID}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, node := range nodes {
		request := registerManagedPhysicalInventory(t, node, parent.StableID)
		result := managedProductRPC[service.ManagedPolicyEnrollmentResult](t, product, "POST", "/enroll", request)
		if !result.Connected || result.LocalPolicyVersion != "1" {
			t.Fatal("actual enrollment unavailable")
		}
		if scope == model.ClientPolicyScopeGlobal && (result.ClientID != parent.StableID || result.PolicyVersion != "7") {
			t.Fatal("global canonical7/local1 mismatch")
		}
		if scope == model.ClientPolicyScopeNode && (result.ClientID == parent.StableID || seen[result.ClientID]) {
			t.Fatal("node scope shares canonical account")
		}
		seen[result.ClientID] = true
	}
	return &managedPhysicalCase{product: product, parent: parent, nodes: nodes}
}

func managedPhysicalDenied(t *testing.T, port int) {
	t.Helper()
	flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Error(err)
		return
	}
	defer flow.Close()
	_ = flow.SetDeadline(time.Now().Add(200 * time.Millisecond))
	_, _ = flow.Write([]byte("x"))
	var buffer [1]byte
	if n, err := flow.Read(buffer[:]); n != 0 || err == nil {
		t.Errorf("exhausted actual quota delivered bytes n=%d err=%v", n, err)
	}
}

func managedPhysicalRateTransfer(t *testing.T, nodes []*managedPhysicalNode, marker byte, count int) time.Duration {
	t.Helper()
	var group sync.WaitGroup
	start := time.Now()
	for _, node := range nodes {
		group.Go(func() {
			flow, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", node.manifest.TunnelPort), time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			defer flow.Close()
			_ = flow.SetDeadline(time.Now().Add(40 * time.Second))
			if marker == 'U' {
				payload := append([]byte{marker}, bytes.Repeat([]byte("u"), count)...)
				if n, err := flow.Write(payload); err != nil || n != len(payload) {
					t.Errorf("actual upload failed n=%d err=%v", n, err)
					return
				}
				if err := flow.(*net.TCPConn).CloseWrite(); err != nil {
					t.Error(err)
					return
				}
				var reply [1]byte
				if n, err := flow.Read(reply[:]); n != 0 || err != io.EOF {
					t.Errorf("actual target did not complete upload: n=%d err=%v", n, err)
				}
			} else {
				if _, err := flow.Write([]byte{marker}); err != nil {
					t.Error(err)
					return
				}
				payload := make([]byte, count)
				if _, err := io.ReadFull(flow, payload); err != nil || !bytes.Equal(payload, bytes.Repeat([]byte("d"), count)) {
					t.Errorf("actual limited download failed: %v", err)
				}
			}
		})
	}
	group.Wait()
	return time.Since(start)
}
