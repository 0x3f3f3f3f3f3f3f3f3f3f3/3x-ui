package policy_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
)

func TestTunnelConfiguredIPv4IPv6AndDomainTargets(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		for _, address := range []string{"127.0.0.1", "::1", "localhost"} {
			t.Run(network+"/"+address, func(t *testing.T) {
				var targetPort int
				received := make(chan []byte, 2)
				bind := address
				if address == "localhost" {
					bind = "127.0.0.1"
				}
				addTarget := func(host string, target int) {
					t.Helper()
					endpoint := net.JoinHostPort(host, strconv.Itoa(target))
					if network == "tcp" {
						listener, err := net.Listen("tcp", endpoint)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = listener.Close() })
						targetPort = listener.Addr().(*net.TCPAddr).Port
						go func() {
							conn, err := listener.Accept()
							if err != nil {
								return
							}
							defer conn.Close()
							_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
							payload := make([]byte, len("target-probe"))
							if _, err := io.ReadFull(conn, payload); err == nil {
								received <- payload
								_, _ = conn.Write(payload)
								_, _ = io.Copy(io.Discard, conn)
							}
						}()
					} else {
						listener, err := net.ListenPacket("udp", endpoint)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = listener.Close() })
						targetPort = listener.LocalAddr().(*net.UDPAddr).Port
						go func() {
							payload := make([]byte, 65535)
							n, peer, err := listener.ReadFrom(payload)
							if err == nil {
								received <- append([]byte(nil), payload[:n]...)
								_, _ = listener.WriteTo(payload[:n], peer)
							}
						}()
					}
				}
				addTarget(bind, 0)
				if address == "localhost" {
					// Resolve either loopback family without depending on host resolver order.
					addTarget("::1", targetPort)
				}
				listen := port(t)
				config := loopbackTunnelConfig(0, network, false, listen, targetPort)
				config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)["rewriteAddress"] = address
				instance := start(t, loopbackJSON(t, config))
				flow := loopbackFlow(t, network, listen)
				payload := []byte("target-probe")
				exchange(t, flow, payload)
				select {
				case actual := <-received:
					if !bytes.Equal(actual, payload) {
						t.Fatal("configured target payload differs")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("configured target did not receive payload")
				}
				engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
				state, err := engine.Snapshot("owner")
				if err != nil || state.Usage != (clientpolicy.Usage{RawUpload: 12, RawDownload: 12, BilledBytes: 36}) {
					t.Fatalf("target selection bypassed accounting: %+v %v", state, err)
				}
			})
		}
	}
}

func TestTunnelTCPHalfCloseRetainsReplyAndLedger(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed-%t", managed), func(t *testing.T) {
			target, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = target.Close() })
			go func() {
				conn, err := target.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				payload, err := io.ReadAll(conn)
				if err == nil {
					_, _ = conn.Write(payload)
				}
			}()
			listen := port(t)
			config := loopbackTunnelConfig(0, "tcp", false, listen, target.Addr().(*net.TCPAddr).Port)
			if !managed {
				delete(config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any), "clientId")
			}
			instance := start(t, loopbackJSON(t, config))
			flow, err := net.DialTCP("tcp4", nil, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: listen})
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			_ = flow.SetDeadline(time.Now().Add(2 * time.Second))
			payload := []byte("after-fin")
			if _, err := flow.Write(payload); err != nil {
				t.Fatal(err)
			}
			if err := flow.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			reply, err := io.ReadAll(flow)
			if err != nil || !bytes.Equal(reply, payload) {
				t.Fatalf("Tunnel half-close lost the target reply: n=%d err=%v", len(reply), err)
			}
			if managed {
				engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
				state, err := engine.Snapshot("owner")
				if err != nil || state.Usage != (clientpolicy.Usage{RawUpload: 9, RawDownload: 9, BilledBytes: 27}) {
					t.Fatalf("half-close reply was not billed once: %+v %v", state, err)
				}
			}
		})
	}
}
