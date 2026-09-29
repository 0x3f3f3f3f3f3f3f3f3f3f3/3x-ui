package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestLocalRuntimeUsesPrivateControlForHandlersRoutingAndStats(t *testing.T) {
	dir, err := os.MkdirTemp("", "runtime-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket, state := filepath.Join(dir, "core.sock"), filepath.Join(dir, "state.db")
	if err := clientpolicy.CreateStore(state, "runtime-test"); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"private","listen":%q,"services":["ClientPolicyServiceV1","HandlerService","StatsService","RoutingService"]},"clientPolicy":{"stateFile":%q,"instanceId":"runtime-test"},"stats":{},"routing":{},"policy":{"system":{"statsInboundUplink":true,"statsInboundDownlink":true}},"outbounds":[{"tag":"direct","protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}},{"tag":"blocked","protocol":"blackhole"}]}`, socket, state)
	var cfg conf.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	built, err := cfg.Build()
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.New(built)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	var panelConfig xray.Config
	if err := json.Unmarshal([]byte(raw), &panelConfig); err != nil {
		t.Fatal(err)
	}
	process := xray.NewProcess(&panelConfig)
	local := NewLocal(LocalDeps{APIEndpoint: process.GetAPIEndpoint})
	var api xray.XrayAPI
	if err := api.InitProcess(process); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	policyAPI, err := xray.DialClientPolicy(ctx, socket, "runtime-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = policyAPI.Close() })
	if err := policyAPI.Apply(ctx, []*clientpolicy.PolicyConfig{{ClientId: "owner", Version: 1, Enabled: true, MultiplierMicros: 1000000, BurstBytes: 65536}}); err != nil {
		t.Fatal(err)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	go func() {
		conn, err := target.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(conn, conn)
		}
	}()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	ib := &model.Inbound{
		Tag: "owned", Protocol: model.Tunnel, Listen: "127.0.0.1", Port: port, Enable: true,
		Settings: fmt.Sprintf(`{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"owner"}`, target.Addr().(*net.TCPAddr).Port),
	}
	if err := local.AddInbound(ctx, ib); err != nil {
		t.Fatal(err)
	}
	if _, _, err := api.GetTraffic(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	payload := bytes.Repeat([]byte{0x53}, 256)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
		t.Fatalf("runtime-created Tunnel payload: %x, error=%v", reply, err)
	}
	traffic, _, err := api.GetTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].Tag != "owned" || traffic[0].Up != 256 || traffic[0].Down != 256 {
		t.Fatalf("private stats did not observe Tunnel traffic: %+v", traffic)
	}
	if err := api.ApplyRoutingConfig([]byte(`{"rules":[{"type":"field","inboundTag":["owned"],"outboundTag":"blocked"}]}`)); err != nil {
		t.Fatal(err)
	}
	blocked, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	_ = blocked.SetDeadline(time.Now().Add(time.Second))
	_, _ = blocked.Write(payload)
	if n, err := blocked.Read(reply); n != 0 || err == nil {
		t.Fatalf("private routing update failed to block payload: n=%d, error=%v", n, err)
	}
	if err := local.DelInbound(ctx, ib); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(reply); err == nil || os.IsTimeout(err) {
		t.Fatalf("Runtime deletion retained the established Tunnel stream: %v", err)
	}
	if unexpected, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
		unexpected.Close()
		t.Fatal("removed Tunnel still accepts connections")
	}
}
