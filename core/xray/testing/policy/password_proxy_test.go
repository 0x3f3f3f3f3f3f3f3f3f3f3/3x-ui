package policy_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/proxy"
	corehttp "github.com/xtls/xray-core/proxy/http"
	"github.com/xtls/xray-core/proxy/socks"
	netproxy "golang.org/x/net/proxy"
)

func passwordProxyDial(mode string, listen, target int, username, password string) (net.Conn, error) {
	endpoint := fmt.Sprintf("127.0.0.1:%d", listen)
	destination := fmt.Sprintf("127.0.0.1:%d", target)
	if mode == "socks" {
		dialer, err := netproxy.SOCKS5("tcp", endpoint, &netproxy.Auth{User: username, Password: password}, &net.Dialer{Timeout: time.Second})
		if err != nil {
			return nil, err
		}
		return dialer.Dial("tcp", destination)
	}
	conn, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", destination, destination, auth)
	if err == nil {
		var response *http.Response
		response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
		if err == nil && response.StatusCode != http.StatusOK {
			err = fmt.Errorf("proxy authentication returned HTTP %d", response.StatusCode)
		}
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func passwordProxyConfig(t *testing.T, protocol string, authPort, tunnelPort, target int) map[string]any {
	t.Helper()
	config := loopbackTunnelConfig(0, "tcp", false, tunnelPort, target)
	config["inbounds"] = append(config["inbounds"].([]any), map[string]any{
		"tag": "password", "listen": "127.0.0.1", "port": authPort, "protocol": protocol,
		"settings": map[string]any{
			"auth": "password", "udp": true,
			"accounts": []any{map[string]any{
				"user": "authenticated-user", "pass": "test-password",
				"email": "canonical-account", "clientId": "owner",
			}},
		},
	})
	config["routing"] = map[string]any{"rules": []any{
		map[string]any{"type": "field", "inboundTag": []string{"owned", "password"}, "outboundTag": "direct"},
	}}
	return config
}

func assertPasswordProxyClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err := conn.Read(make([]byte, 1))
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("password proxy stream stayed open: %v", err)
	}
}

func TestPasswordProxiesShareTunnelIdentityAndDisconnect(t *testing.T) {
	for _, tc := range []struct{ protocol, client string }{
		{"socks", "socks"}, {"mixed", "socks"}, {"http", "http"}, {"mixed", "http"},
	} {
		t.Run(tc.protocol+"/"+tc.client, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			authPort, tunnelPort := port(t), port(t)
			instance := start(t, loopbackJSON(t, passwordProxyConfig(t, tc.protocol, authPort, tunnelPort, target)))
			for _, invalid := range []struct{ user, pass string }{
				{"authenticated-user", "wrong-password"}, {"owner", "test-password"},
			} {
				conn, err := passwordProxyDial(tc.client, authPort, target, invalid.user, invalid.pass)
				if conn != nil {
					_ = conn.Close()
				}
				if err == nil || received.Load() != 0 {
					t.Fatalf("unverified credential reached target: error=%v received=%d", err, received.Load())
				}
			}
			var flows []net.Conn
			for i := 0; i < 2; i++ {
				flow, err := passwordProxyDial(tc.client, authPort, target, "authenticated-user", "test-password")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = flow.Close() })
				exchange(t, flow, []byte("authed"))
				flows = append(flows, flow)
			}
			tunnel := loopbackFlow(t, "tcp", tunnelPort)
			exchange(t, tunnel, []byte("tunnel"))
			flows = append(flows, tunnel)
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			snapshot, err := engine.Snapshot("owner")
			want := clientpolicy.Usage{RawUpload: 18, RawDownload: 18, BilledBytes: 54}
			if err != nil || snapshot.Usage != want || snapshot.ActiveSessions != 3 || received.Load() != 18 {
				t.Fatalf("authenticated accounts did not share Tunnel identity: %+v err=%v target=%d", snapshot, err, received.Load())
			}
			policy, _, err := engine.GetClient("owner")
			if err != nil {
				t.Fatal(err)
			}
			policy.Version++
			policy.Enabled = false
			if err := engine.Apply(policy); err != nil {
				t.Fatal(err)
			}
			for _, flow := range flows {
				assertPasswordProxyClosed(t, flow)
			}
			if received.Load() != 18 {
				t.Fatalf("disabled account sent extra payload: %d", received.Load())
			}
		})
	}
}

func TestPasswordProxiesWithoutPolicyFailClosed(t *testing.T) {
	for _, tc := range []struct{ protocol, client string }{
		{"socks", "socks"}, {"http", "http"}, {"mixed", "http"},
	} {
		t.Run(tc.protocol+"/"+tc.client, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			authPort := port(t)
			config := passwordProxyConfig(t, tc.protocol, authPort, port(t), target)
			delete(config, "clientPolicy")
			start(t, loopbackJSON(t, config))
			flow, err := passwordProxyDial(tc.client, authPort, target, "authenticated-user", "test-password")
			if err == nil {
				t.Cleanup(func() { _ = flow.Close() })
				if err := flow.SetDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				_, _ = flow.Write([]byte("denied"))
				assertPasswordProxyClosed(t, flow)
			}
			if received.Load() != 0 {
				t.Fatalf("missing policy allowed an unlimited authenticated stream: %d", received.Load())
			}
		})
	}
}

func TestPasswordProxiesCredentialRemovalAndReadd(t *testing.T) {
	for _, tc := range []struct{ protocol, client string }{
		{"socks", "socks"}, {"mixed", "socks"}, {"http", "http"}, {"mixed", "http"},
	} {
		t.Run(tc.protocol+"/"+tc.client, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			authPort, tunnelPort := port(t), port(t)
			instance := start(t, loopbackJSON(t, passwordProxyConfig(t, tc.protocol, authPort, tunnelPort, target)))
			flow, err := passwordProxyDial(tc.client, authPort, target, "authenticated-user", "test-password")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = flow.Close() })
			sibling := loopbackFlow(t, "tcp", tunnelPort)
			exchange(t, flow, []byte("authed"))
			exchange(t, sibling, []byte("tunnel"))
			manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
			handler, err := manager.GetHandler(context.Background(), "password")
			if err != nil {
				t.Fatal(err)
			}
			if err := (&command.RemoveUserOperation{Email: "canonical-account"}).ApplyInbound(context.Background(), handler); err != nil {
				t.Fatalf("native credential removal failed: %v", err)
			}
			assertPasswordProxyClosed(t, flow)
			users := handler.(proxy.GetInbound).GetInbound().(proxy.UserManager)
			if users.GetUsersCount(context.Background()) != 0 || users.GetUser(context.Background(), "canonical-account") != nil {
				t.Fatal("native credential management retained the removed account")
			}
			if denied, err := passwordProxyDial(tc.client, authPort, target, "authenticated-user", "test-password"); err == nil {
				_ = denied.Close()
				t.Fatal("removed credential authenticated again")
			}
			exchange(t, sibling, []byte("siblng"))
			account := serial.ToTypedMessage(&socks.Account{Username: "authenticated-user", Password: "replacement-password"})
			if tc.protocol == "http" {
				account = serial.ToTypedMessage(&corehttp.Account{Username: "authenticated-user", Password: "replacement-password"})
			}
			operation := &command.AddUserOperation{User: &protocol.User{Email: "canonical-account", ClientId: "owner", Account: account}}
			if err := operation.ApplyInbound(context.Background(), handler); err != nil {
				t.Fatal(err)
			}
			fresh, err := passwordProxyDial(tc.client, authPort, target, "authenticated-user", "replacement-password")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			exchange(t, fresh, []byte("newone"))
			snapshot, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot("owner")
			if err != nil || snapshot.Usage != (clientpolicy.Usage{RawUpload: 24, RawDownload: 24, BilledBytes: 72}) || received.Load() != 24 {
				t.Fatalf("credential replacement altered shared history: %+v err=%v target=%d", snapshot, err, received.Load())
			}
			if err := manager.RemoveHandler(context.Background(), "password"); err != nil {
				t.Fatal(err)
			}
			if err := operation.ApplyInbound(context.Background(), handler); err == nil {
				t.Fatal("closed handler accepted an account")
			}
			exchange(t, sibling, []byte("alive!"))
		})
	}
}
