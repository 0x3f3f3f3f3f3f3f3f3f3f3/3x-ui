package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	clientapi "github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/enfein/mieru/v3/pkg/stderror"
	"google.golang.org/protobuf/proto"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestMieruInboundRunsThroughProductionXrayLifecycle(t *testing.T) {
	runMieruInboundProductionXrayLifecycle(t, false)
}

func runMieruInboundProductionXrayLifecycle(t *testing.T, postgres bool) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			if postgres {
				managedUsagePostgresSchema(t)
			}
			fixture := newProductionMieruFixture(t, underlay)
			client, user, udpTarget := fixture.client, fixture.user, fixture.udpTarget
			for _, network := range []string{"tcp", "udp"} {
				destination := apimodel.NetAddrSpec{Net: network, AddrSpec: apimodel.AddrSpec{FQDN: "route.invalid", Port: 443}}
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				conn, err := client.DialContext(ctx, destination)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				payload := bytes.Repeat([]byte{42}, 8193)
				response := make([]byte, len(payload))
				if network == "tcp" {
					if _, err := conn.Write(payload); err != nil {
						t.Fatal(err)
					}
					if _, err := io.ReadFull(conn, response); err != nil {
						t.Fatal(err)
					}
				} else {
					packets := apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
					if _, err := packets.WriteTo(payload, destination); err != nil {
						t.Fatal(err)
					}
					n, peer, err := packets.ReadFrom(response)
					if err != nil || n != len(payload) || peer.String() != udpTarget.String() {
						t.Fatalf("public UDP payload or actual reply peer lost: n=%d peer=%v err=%v", n, peer, err)
					}
				}
				if !bytes.Equal(payload, response) {
					t.Fatal("public mieru path changed payload")
				}
			}
			record := lookupClientRecord(t, user.Email)
			if record.ExpiryTime <= time.Now().UnixMilli() {
				t.Fatal("authenticated mieru use did not activate delayed expiry")
			}
			account, err := database.NewClientUsageLedger(database.GetDB()).Read(t.Context(), record.PolicyID)
			if err != nil || account.Up != 16386 || account.Down != 16386 || account.Billed != 49158 {
				t.Fatalf("public routed mieru payload was not billed once at 1.5x: %+v, %v", account, err)
			}
		})
	}
}

func TestMieruInboundRunsThroughProductionXrayLifecycle_Postgres(t *testing.T) {
	runMieruInboundProductionXrayLifecycle(t, true)
}

func TestMieruInboundProtocolChangeRevokesOldRuntime(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			fixture := newProductionMieruFixture(t, underlay)
			flows := openProductionMieruFlows(t, fixture.client)
			var originalTraffic xray.ClientTraffic
			if err := database.GetDB().Where("email = ?", fixture.user.Email).First(&originalTraffic).Error; err != nil {
				t.Fatal(err)
			}
			inbounds := &InboundService{}
			updated, err := inbounds.GetInbound(fixture.inbound.Id)
			if err != nil {
				t.Fatal(err)
			}
			updated.Protocol, updated.Settings = model.VLESS, `{"clients":[],"decryption":"none"}`
			updated.StreamSettings = `{"network":"tcp","security":"none"}`
			if _, _, err := inbounds.UpdateInbound(updated); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruClosed(t, flows)
			var detachedTraffic xray.ClientTraffic
			if err := database.GetDB().Where("email = ?", fixture.user.Email).First(&detachedTraffic).Error; err != nil || detachedTraffic.PolicyID != originalTraffic.PolicyID || detachedTraffic.Up != originalTraffic.Up || detachedTraffic.Down != originalTraffic.Down {
				t.Fatalf("protocol conversion lost the surviving owner's accounting projection: err=%v up=%d down=%d", err, detachedTraffic.Up, detachedTraffic.Down)
			}
			requireProductionMieruDenied(t, fixture.client)
			if err := fixture.service.RestartXray(false); err != nil {
				t.Fatal(err)
			}
			productionSSHWait(t, fixture.address.String())
			settings, err := json.Marshal(map[string]any{"network": underlay, "clients": []model.Client{fixture.user}})
			if err != nil {
				t.Fatal(err)
			}
			updated.Protocol, updated.Settings = model.Mieru, string(settings)
			if _, _, err := inbounds.UpdateInbound(updated); err != nil {
				t.Fatal(err)
			}
			if probe, err := net.DialTimeout("tcp", fixture.address.String(), 100*time.Millisecond); err == nil {
				_ = probe.Close()
				t.Fatal("the prior Xray listener survived conversion back to native mieru")
			}
			if err := fixture.service.RestartXray(false); err != nil {
				t.Fatal(err)
			}
			waitProductionMieru(t, updated.Id)
			record := lookupClientRecord(t, fixture.user.Email)
			var traffic xray.ClientTraffic
			if err := database.GetDB().Where("email = ?", fixture.user.Email).First(&traffic).Error; err != nil {
				t.Fatal(err)
			}
			if !record.Enable || !traffic.Enable {
				t.Fatalf("protocol conversion disabled the returned owner: canonical=%t traffic=%t", record.Enable, traffic.Enable)
			}
			recovered := productionMieruClient(t, underlay, fixture.address, fixture.user)
			for _, flow := range openProductionMieruFlows(t, recovered) {
				flow.echo(t)
			}
		})
	}
}

func TestMieruInboundHotCredentialsAndEnablePreserveOtherClient(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			fixture := newProductionMieruFixture(t, underlay)
			clients, inbounds := &ClientService{}, &InboundService{}
			peer := model.Client{Email: "mieru-unaffected", Password: "independent-runtime-password", Enable: true}
			if _, err := clients.CreateOne(inbounds, fixture.inbound.Id, peer); err != nil {
				t.Fatal(err)
			}
			if err := fixture.service.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			waitProductionMieru(t, fixture.inbound.Id)
			other := productionMieruClient(t, underlay, fixture.address, peer)
			original := openProductionMieruFlows(t, fixture.client)
			unaffected := openProductionMieruFlows(t, other)
			record := lookupClientRecord(t, fixture.user.Email)
			updated := *record.ToClient()
			updated.Password = "rotated-runtime-password"
			if _, err := clients.Update(inbounds, record.Id, updated, 0); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruClosed(t, original)
			oldCredential := productionMieruClient(t, underlay, fixture.address, fixture.user)
			requireProductionMieruDenied(t, oldCredential)
			current := productionMieruClient(t, underlay, fixture.address, updated)
			live := openProductionMieruFlows(t, current)
			for _, flow := range unaffected {
				flow.echo(t)
			}
			if _, _, err := clients.SetClientEnableByEmail(inbounds, updated.Email, false); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruClosed(t, live)
			requireProductionMieruDenied(t, current)
			for _, flow := range unaffected {
				flow.echo(t)
			}
			if _, _, err := clients.SetClientEnableByEmail(inbounds, updated.Email, true); err != nil {
				t.Fatal(err)
			}
			openProductionMieruFlows(t, current)
			for _, flow := range unaffected {
				flow.echo(t)
			}
		})
	}
}

func TestMieruInboundQuotaSurvivesCoreRestartAndManualDisable(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			fixture := newProductionMieruFixture(t, underlay)
			clients, inbounds := &ClientService{}, &InboundService{}
			live := openProductionMieruFlows(t, fixture.client)
			record := lookupClientRecord(t, fixture.user.Email)
			ledger := database.NewClientUsageLedger(database.GetDB())
			before, err := ledger.Read(t.Context(), record.PolicyID)
			if err != nil {
				t.Fatal(err)
			}
			updated := *record.ToClient()
			updated.TotalGB = before.Billed
			if _, err := clients.Update(inbounds, record.Id, updated, 0); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruClosed(t, live)
			requireProductionMieruDenied(t, fixture.client)
			if err := fixture.service.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			waitProductionMieru(t, fixture.inbound.Id)
			restarted := productionMieruClient(t, underlay, fixture.address, updated)
			requireProductionMieruDenied(t, restarted)
			after, err := ledger.Read(t.Context(), record.PolicyID)
			if err != nil || after.Up != before.Up || after.Down != before.Down || after.Billed != before.Billed {
				t.Fatalf("exhausted restart changed durable usage: before=%+v after=%+v err=%v", before, after, err)
			}
			if _, _, err := clients.SetClientEnableByEmail(inbounds, updated.Email, false); err != nil {
				t.Fatal(err)
			}
			if err := inbounds.ResetClientTrafficByEmail(updated.Email); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruDenied(t, restarted)
			if _, _, err := clients.SetClientEnableByEmail(inbounds, updated.Email, true); err != nil {
				t.Fatal(err)
			}
			reloaded := lookupClientRecord(t, updated.Email)
			updated = *reloaded.ToClient()
			updated.TotalGB = 0
			if _, err := clients.Update(inbounds, record.Id, updated, 0); err != nil {
				t.Fatal(err)
			}
			openProductionMieruFlows(t, restarted)
		})
	}
}

func TestMieruInboundCoreFailureClosesFlowsAndRecovers(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			fixture := newProductionMieruFixture(t, underlay)
			live := openProductionMieruFlows(t, fixture.client)
			if err := currentXrayProcess().Stop(); err != nil {
				t.Fatal(err)
			}
			requireProductionMieruClosed(t, live)
			requireProductionMieruDenied(t, fixture.client)
			if underlay == "tcp" {
				listener, err := net.Listen("tcp", fixture.address.String())
				if err != nil {
					t.Fatalf("failed core left native TCP listener open: %v", err)
				}
				_ = listener.Close()
			} else {
				listener, err := net.ListenPacket("udp", fixture.address.String())
				if err != nil {
					t.Fatalf("failed core left native UDP listener open: %v", err)
				}
				_ = listener.Close()
			}
			if err := fixture.service.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			waitProductionMieru(t, fixture.inbound.Id)
			restarted := productionMieruClient(t, underlay, fixture.address, fixture.user)
			openProductionMieruFlows(t, restarted)
		})
	}
}

type productionMieruFlow struct {
	conn        net.Conn
	packets     net.PacketConn
	destination apimodel.NetAddrSpec
}

func openProductionMieruFlows(t *testing.T, client clientapi.Client) []productionMieruFlow {
	t.Helper()
	var flows []productionMieruFlow
	for _, network := range []string{"tcp", "udp"} {
		destination := apimodel.NetAddrSpec{Net: network, AddrSpec: apimodel.AddrSpec{FQDN: "route.invalid", Port: 443}}
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		done := make(chan struct{})
		timer := time.AfterFunc(2*time.Second, func() { cancel(); _ = client.Stop(); close(done) })
		deadline := time.Now().Add(2 * time.Second)
		var conn net.Conn
		var err, firstErr error
		for {
			conn, err = client.DialContext(ctx, destination)
			if firstErr == nil && err != nil {
				firstErr = err
			}
			if err == nil || time.Now().After(deadline) {
				break
			}
			if conn != nil {
				_ = conn.Close()
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !timer.Stop() {
			<-done
		}
		if err != nil {
			if conn != nil {
				_ = conn.Close()
			}
			t.Fatalf("native %s admission failed: first=%v final=%v", network, firstErr, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		flow := productionMieruFlow{conn: conn, destination: destination}
		if network == "udp" {
			flow.packets = apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn))
		}
		flow.echo(t)
		flows = append(flows, flow)
	}
	return flows
}

func (flow productionMieruFlow) echo(t *testing.T) {
	t.Helper()
	_ = flow.conn.SetDeadline(time.Now().Add(2 * time.Second))
	payload, response := []byte("public-runtime-payload"), make([]byte, 22)
	if flow.packets == nil {
		if _, err := flow.conn.Write(payload); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(flow.conn, response); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := flow.packets.WriteTo(payload, flow.destination); err != nil {
			t.Fatal(err)
		}
		n, _, err := flow.packets.ReadFrom(response)
		if err != nil || n != len(payload) {
			t.Fatalf("UDP echo: bytes=%d err=%v", n, err)
		}
	}
	if !bytes.Equal(payload, response) {
		t.Fatalf("public payload changed: %q", response)
	}
}

func requireProductionMieruClosed(t *testing.T, flows []productionMieruFlow) {
	t.Helper()
	deadline := time.Now().Add(1250 * time.Millisecond)
	for _, flow := range flows {
		_ = flow.conn.SetReadDeadline(deadline)
		var b [1]byte
		n, err := flow.conn.Read(b[:])
		var timeout net.Error
		if n != 0 || err == nil || errors.Is(err, stderror.ErrTimeout) || (errors.As(err, &timeout) && timeout.Timeout()) {
			t.Fatalf("revoked public %s flow remained open past cutoff: bytes=%d err=%v", flow.destination.Net, n, err)
		}
	}
}

func requireProductionMieruDenied(t *testing.T, client clientapi.Client) {
	t.Helper()
	config, err := client.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, network := range []string{"tcp", "udp"} {
		probe := clientapi.NewClient()
		if err := probe.Store(config); err != nil {
			t.Fatal(err)
		}
		if err := probe.Start(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 750*time.Millisecond)
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { _ = probe.Stop(); close(done) })
		conn, err := probe.DialContext(ctx, apimodel.NetAddrSpec{Net: network, AddrSpec: apimodel.AddrSpec{FQDN: "route.invalid", Port: 443}})
		if !stop() {
			<-done
		}
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		_ = probe.Stop()
		if err == nil {
			t.Fatalf("revoked public credential opened a new %s flow", network)
		}
	}
}

func waitProductionMieru(t *testing.T, id int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mieruRuntimeState.Lock()
		m := mieruRuntimeState.manager
		mieruRuntimeState.Unlock()
		if m != nil {
			m.mu.Lock()
			entry := m.entries[id]
			ready := entry != nil && entry.server != nil && entry.lastError == ""
			m.mu.Unlock()
			if ready {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("public mieru listener did not become ready within two seconds")
}

type productionMieruFixture struct {
	service   *XrayService
	inbound   *model.Inbound
	user      model.Client
	client    clientapi.Client
	address   netip.AddrPort
	udpTarget net.Addr
}

func newProductionMieruFixture(t *testing.T, underlay string) productionMieruFixture {
	t.Helper()
	return newProductionMieruFixtureWithTargets(t, underlay, func(t *testing.T) (net.Addr, net.Addr) {
		return productionMieruEchoTarget(t, "tcp"), productionMieruEchoTarget(t, "udp")
	})
}

func newProductionMieruFixtureWithTargets(t *testing.T, underlay string, targets func(*testing.T) (net.Addr, net.Addr)) productionMieruFixture {
	t.Helper()
	binary := os.Getenv("XUI_MANAGED_XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XUI_MANAGED_XRAY_E2E_BINARY for public mieru Runtime data paths")
	}
	setupConflictDB(t)
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	t.Setenv("XUI_LOG_FOLDER", filepath.Join(binDir, "logs"))
	if err := os.Symlink(binary, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	tcpTarget, udpTarget := targets(t)
	api := netip.MustParseAddrPort(productionSSHAddress(t))
	template := map[string]any{
		"log":      map[string]any{"loglevel": "warning"},
		"api":      map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"stats":    map[string]any{},
		"inbounds": []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": api.Port(), "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}},
		"outbounds": []any{
			map[string]any{"tag": "blocked", "protocol": "blackhole"},
			map[string]any{"tag": "tcp-echo", "protocol": "freedom", "settings": map[string]any{"redirect": tcpTarget.String()}},
			map[string]any{"tag": "udp-echo", "protocol": "freedom", "settings": map[string]any{"redirect": udpTarget.String()}},
		},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
			map[string]any{"type": "field", "domain": []string{"full:route.invalid"}, "network": "tcp", "outboundTag": "tcp-echo"},
			map[string]any{"type": "field", "domain": []string{"full:route.invalid"}, "network": "udp", "outboundTag": "udp-echo"},
		}},
	}
	encoded, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", string(encoded)); err != nil {
		t.Fatal(err)
	}
	svc := &XrayService{}
	isManuallyStopped.Store(false)
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: svc.GetXrayAPIPort, SetNeedRestart: svc.SetToNeedRestart, SSHChanged: NotifySSHChange, MieruChanged: NotifyMieruChange}))
	t.Cleanup(func() {
		_ = svc.StopXray()
		runtime.SetManager(nil)
		isManuallyStopped.Store(false)
		isNeedXrayRestart.Store(false)
	})
	address := netip.MustParseAddrPort(productionSSHAddress(t))
	user := model.Client{Email: "mieru-public", Password: "owned-runtime-test-password", Enable: true, ExpiryTime: -int64(time.Hour / time.Millisecond)}
	settings, err := json.Marshal(map[string]any{"network": underlay, "clients": []model.Client{user}})
	if err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{Protocol: model.Mieru, Enable: true, Listen: "127.0.0.1", Port: int(address.Port()), Settings: string(settings)}
	if _, _, err := (&InboundService{}).AddInbound(inbound); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	if lookupClientRecord(t, user.Email).ExpiryTime >= 0 {
		t.Fatal("starting mieru consumed delayed first-use expiry")
	}
	policy, err := (&ClientService{}).GetPolicy(t.Context(), user.Email)
	if err != nil || !policy.Supported {
		t.Fatalf("public mieru policy API is unavailable: %+v, %v", policy, err)
	}
	policy.Multiplier = "1.5"
	if _, err := (&ClientService{}).UpdatePolicy(t.Context(), user.Email, policy.ClientPolicyUpdate); err != nil {
		t.Fatal(err)
	}
	client := productionMieruClient(t, underlay, address, user)
	time.Sleep(300 * time.Millisecond)
	return productionMieruFixture{service: svc, inbound: inbound, user: user, client: client, address: address, udpTarget: udpTarget}
}

func productionMieruClient(t *testing.T, underlay string, address netip.AddrPort, user model.Client, multiplexing ...appctlpb.MultiplexingLevel) clientapi.Client {
	t.Helper()
	client := clientapi.NewClient()
	transport := appctlpb.TransportProtocol_TCP
	if underlay == "udp" {
		transport = appctlpb.TransportProtocol_UDP
	}
	profile := &appctlpb.ClientProfile{ProfileName: proto.String("public-runtime"), User: &appctlpb.User{Name: proto.String(user.Email), Password: proto.String(user.Password)}, Servers: []*appctlpb.ServerEndpoint{{IpAddress: proto.String(address.Addr().String()), PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(address.Port())), Protocol: transport.Enum()}}}}}
	if len(multiplexing) > 0 {
		profile.Multiplexing = &appctlpb.MultiplexingConfig{Level: multiplexing[0].Enum()}
	}
	if err := client.Store(&clientapi.ClientConfig{Profile: profile}); err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })
	return client
}

func productionMieruEchoTarget(t *testing.T, network string) net.Addr {
	t.Helper()
	return productionMieruEchoTargetAt(t, network, "127.0.0.1:0")
}

func productionMieruEchoTargetAt(t *testing.T, network, address string) net.Addr {
	t.Helper()
	if network == "udp" {
		conn, err := net.ListenPacket("udp", address)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			buffer := make([]byte, 65535)
			for {
				n, peer, err := conn.ReadFrom(buffer)
				if err != nil {
					return
				}
				_, _ = conn.WriteTo(buffer[:n], peer)
			}
		}()
		t.Cleanup(func() { _ = conn.Close(); <-done })
		return conn.LocalAddr()
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				_, _ = io.Copy(conn, conn)
			})
		}
	})
	t.Cleanup(func() { cancel(); _ = listener.Close(); workers.Wait() })
	return listener.Addr()
}
