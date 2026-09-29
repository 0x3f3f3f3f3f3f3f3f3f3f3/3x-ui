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
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
	"github.com/xtls/xray-core/proxy/vless"
	vlessencoding "github.com/xtls/xray-core/proxy/vless/encoding"

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
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
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
	t.Run("managed-user-credential-rotation", func(t *testing.T) {
		before, err := policyAPI.GetClient(ctx, "owner")
		if err != nil {
			t.Fatal(err)
		}
		auth := &model.Inbound{Tag: "authenticated", Protocol: model.VLESS, Listen: "127.0.0.1", Port: port, Enable: true, Settings: `{"decryption":"none","clients":[]}`}
		if err := local.AddInbound(ctx, auth); err != nil {
			t.Fatal(err)
		}
		const oldID = "936997e1-3b0c-4de9-9eea-047ee5829d3e"
		const newID = "01dc4f70-3902-446a-98cb-c00992d1a6c5"
		open := func(id string, size int) (net.Conn, error) {
			user, err := (&protocol.User{Account: serial.ToTypedMessage(&vless.Account{Id: id})}).ToMemoryUser()
			if err != nil {
				return nil, err
			}
			c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
			if err != nil {
				return nil, err
			}
			t.Cleanup(func() { _ = c.Close() })
			_ = c.SetDeadline(time.Now().Add(time.Second))
			request := &protocol.RequestHeader{User: user, Command: protocol.RequestCommandTCP, Address: xnet.LocalHostIP, Port: xnet.Port(target.Addr().(*net.TCPAddr).Port)}
			if err := vlessencoding.EncodeRequestHeader(c, request, &vlessencoding.Addons{}); err != nil {
				return c, err
			}
			payload := bytes.Repeat([]byte{0x62}, size)
			if _, err := c.Write(payload); err != nil {
				return c, err
			}
			if _, err := vlessencoding.DecodeResponseHeader(c, request); err != nil {
				return c, err
			}
			reply := make([]byte, size)
			if _, err := io.ReadFull(c, reply); err != nil {
				return c, err
			}
			if !bytes.Equal(reply, payload) {
				return c, fmt.Errorf("authenticated payload changed")
			}
			return c, nil
		}
		if err := local.AddUser(ctx, auth, map[string]any{"email": "old-display", "id": oldID, "clientId": "owner"}); err != nil {
			t.Fatal(err)
		}
		first, err := open(oldID, 32)
		if err != nil {
			t.Fatal(err)
		}
		_ = first.Close()
		if err := local.RemoveUser(ctx, auth, "old-display"); err != nil {
			t.Fatal(err)
		}
		if err := local.AddUser(ctx, auth, map[string]any{"email": "new-display", "id": newID, "clientId": "owner"}); err != nil {
			t.Fatal(err)
		}
		if _, err := open(oldID, 11); err == nil {
			t.Fatal("rotated credential still authenticates")
		}
		current, err := open(newID, 17)
		if err != nil {
			t.Fatal(err)
		}
		after, err := policyAPI.GetClient(ctx, "owner")
		if err != nil || after.Policy.Version != 1 || after.Usage.RawUpload != before.Usage.RawUpload+49 || after.Usage.RawDownload != before.Usage.RawDownload+49 || after.Usage.BilledBytes != before.Usage.BilledBytes+98 {
			t.Fatalf("Runtime account rotation lost or reassigned usage: %+v, %v", after, err)
		}
		if err := policyAPI.Apply(ctx, []*clientpolicy.PolicyConfig{{ClientId: "owner", Version: 2, Enabled: false, MultiplierMicros: 1000000, BurstBytes: 65536}}); err != nil {
			t.Fatal(err)
		}
		_ = current.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := current.Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
			t.Fatalf("disable retained Runtime-authenticated connection: %v", err)
		}
	})
}
