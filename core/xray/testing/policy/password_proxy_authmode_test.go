package policy_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/stats"
	corehttp "github.com/xtls/xray-core/proxy/http"
	"github.com/xtls/xray-core/proxy/socks"
	netproxy "golang.org/x/net/proxy"
)

func passwordProxyAnonymousDial(mode string, listen, target int) (net.Conn, error) {
	endpoint := fmt.Sprintf("127.0.0.1:%d", listen)
	destination := fmt.Sprintf("127.0.0.1:%d", target)
	if mode == "socks" {
		dialer, err := netproxy.SOCKS5("tcp", endpoint, nil, &net.Dialer{Timeout: time.Second})
		if err != nil {
			return nil, err
		}
		return dialer.Dial("tcp", destination)
	}
	conn, err := net.DialTimeout("tcp", endpoint, time.Second)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(time.Second))
	if mode == "socks4" {
		_, err = conn.Write([]byte{4, 1, byte(target >> 8), byte(target), 127, 0, 0, 1, 0})
		response := make([]byte, 8)
		if err == nil {
			_, err = io.ReadFull(conn, response)
		}
		if err == nil && response[1] != 90 {
			err = fmt.Errorf("SOCKS4 request rejected: %x", response)
		}
	} else {
		_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", destination, destination)
		if err == nil {
			var response *http.Response
			response, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
			if err == nil && response.StatusCode != http.StatusOK {
				err = fmt.Errorf("HTTP authentication required: %d", response.StatusCode)
			}
		}
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return conn, nil
}

func TestPasswordProxiesEmptyAccountsStayAuthenticated(t *testing.T) {
	for _, tc := range []struct{ protocol, client string }{
		{"socks", "socks"}, {"mixed", "socks"}, {"http", "http"}, {"mixed", "http"}, {"mixed", "socks4"},
	} {
		t.Run(tc.protocol+"/"+tc.client, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			authPort := port(t)
			config := passwordProxyConfig(t, tc.protocol, authPort, port(t), target)
			settings := config["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)
			settings["accounts"] = []any{}
			settings["requireAuthentication"] = true
			instance := start(t, loopbackJSON(t, config))
			manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
			handler, err := manager.GetHandler(context.Background(), "password")
			if err != nil {
				t.Fatal(err)
			}
			for _, empty := range []string{"startup", "last-user-removed"} {
				if conn, err := passwordProxyAnonymousDial(tc.client, authPort, target); err == nil {
					conn.Close()
					t.Fatalf("%s password listener accepted anonymous authentication", empty)
				}
				if empty == "startup" {
					account := serial.ToTypedMessage(&socks.Account{Username: "authenticated-user", Password: "test-password"})
					if tc.protocol == "http" {
						account = serial.ToTypedMessage(&corehttp.Account{Username: "authenticated-user", Password: "test-password"})
					}
					if err := (&command.AddUserOperation{User: &protocol.User{ClientId: "owner", Email: "canonical-account", Account: account}}).ApplyInbound(context.Background(), handler); err != nil {
						t.Fatal(err)
					}
					client := tc.client
					if client == "socks4" {
						client = "socks"
					}
					conn, err := passwordProxyDial(client, authPort, target, "authenticated-user", "test-password")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { conn.Close() })
					exchange(t, conn, []byte("authed"))
					if err := (&command.RemoveUserOperation{Email: "canonical-account"}).ApplyInbound(context.Background(), handler); err != nil {
						t.Fatal(err)
					}
					assertPasswordProxyClosed(t, conn)
				}
			}
			if received.Load() != 6 {
				t.Fatalf("anonymous request reached target: %d", received.Load())
			}
		})
	}
}

func TestPasswordProxiesPreserveLegacyAnonymousAndLabels(t *testing.T) {
	for _, tc := range []struct{ protocol, client string }{
		{"socks", "socks"}, {"mixed", "socks4"}, {"mixed", "http"}, {"http", "http"},
	} {
		for _, authenticated := range []bool{false, true} {
			if authenticated && tc.client == "socks4" {
				continue
			}
			t.Run(fmt.Sprintf("%s/%s/password=%t", tc.protocol, tc.client, authenticated), func(t *testing.T) {
				origin, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { origin.Close() })
				received := new(atomic.Int64)
				go func() {
					conn, err := origin.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					payload := make([]byte, 6)
					n, err := io.ReadFull(conn, payload)
					received.Add(int64(n))
					if err == nil {
						conn.Write(payload)
					}
				}()
				target := origin.Addr().(*net.TCPAddr).Port
				authPort := port(t)
				config := passwordProxyConfig(t, tc.protocol, authPort, port(t), target)
				delete(config, "clientPolicy")
				settings := config["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)
				settings["auth"] = "noauth"
				settings["accounts"] = []any{}
				if authenticated {
					settings["auth"] = "password"
					settings["accounts"] = []any{map[string]any{"user": "authenticated-user", "pass": "test-password", "email": "ignored-legacy-email"}}
				}
				instance := start(t, loopbackJSON(t, config))
				var conn net.Conn
				err = nil
				if authenticated {
					conn, err = passwordProxyDial(tc.client, authPort, target, "authenticated-user", "test-password")
				} else {
					conn, err = passwordProxyAnonymousDial(tc.client, authPort, target)
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { conn.Close() })
				exchange(t, conn, []byte("legacy"))
				if received.Load() != 6 {
					t.Fatalf("legacy payload count: %d", received.Load())
				}
				if authenticated {
					// Legacy Linux splice commits its counters when ReadFrom exits.
					conn.Close()
					manager := instance.GetFeature(stats.ManagerType()).(stats.Manager)
					for _, direction := range []string{"uplink", "downlink"} {
						counter := manager.GetCounter("user>>>authenticated-user>>>traffic>>>" + direction)
						deadline := time.Now().Add(time.Second)
						for counter != nil && counter.Value() < 6 && time.Now().Before(deadline) {
							time.Sleep(time.Millisecond)
						}
						if counter == nil {
							t.Fatalf("legacy %s counter is missing", direction)
						}
						if counter.Value() != 6 || manager.GetCounter("user>>>ignored-legacy-email>>>traffic>>>"+direction) != nil {
							t.Fatalf("legacy %s label changed: value=%d", direction, counter.Value())
						}
					}
				} else {
					manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
					handler, err := manager.GetHandler(context.Background(), "password")
					if err != nil {
						t.Fatal(err)
					}
					account := serial.ToTypedMessage(&socks.Account{Username: "new-user", Password: "new-password"})
					if tc.protocol == "http" {
						account = serial.ToTypedMessage(&corehttp.Account{Username: "new-user", Password: "new-password"})
					}
					if err := (&command.AddUserOperation{User: &protocol.User{Account: account}}).ApplyInbound(context.Background(), handler); err == nil {
						t.Fatal("native account creation silently changed anonymous authentication mode")
					}
				}
			})
		}
	}
}

func TestPasswordMixedLegacyNoAuthIgnoresUnusedAccounts(t *testing.T) {
	target, received := loopbackEchoTarget(t, "tcp")
	authPort := port(t)
	config := passwordProxyConfig(t, "mixed", authPort, port(t), target)
	delete(config, "clientPolicy")
	settings := config["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)
	settings["auth"] = "noauth"
	settings["accounts"] = []any{map[string]any{"user": "authenticated-user", "pass": "test-password"}}
	start(t, loopbackJSON(t, config))
	socks, err := passwordProxyAnonymousDial("socks", authPort, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socks.Close() })
	exchange(t, socks, []byte("legacy"))
	anonymous, err := passwordProxyAnonymousDial("http", authPort, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { anonymous.Close() })
	exchange(t, anonymous, []byte("noauth"))
	// These credentials are irrelevant in the pinned upstream's noauth mode.
	http, err := passwordProxyDial("http", authPort, target, "authenticated-user", "incorrect-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { http.Close() })
	exchange(t, http, []byte("authed"))
	if received.Load() != 18 {
		t.Fatalf("legacy Mixed branches changed target payload: %d", received.Load())
	}
}
