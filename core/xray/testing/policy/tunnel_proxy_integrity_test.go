package policy_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
)

// The independent reference proxies propagate EOF themselves. Core upstreams
// additionally verify that a second inbound preserves the same TCP semantics.
func tunnelEOFProxy(t *testing.T, protocol string) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		var address string
		if protocol == "socks" {
			greeting := make([]byte, 2)
			if _, err = io.ReadFull(reader, greeting); err != nil {
				return
			}
			if _, err = io.CopyN(io.Discard, reader, int64(greeting[1])); err != nil {
				return
			}
			if _, err = conn.Write([]byte{5, 0}); err != nil {
				return
			}
			header := make([]byte, 4)
			if _, err = io.ReadFull(reader, header); err != nil {
				return
			}
			if header[0] != 5 || header[1] != 1 || header[3] != 1 {
				return
			}
			endpoint := make([]byte, 6)
			if _, err = io.ReadFull(reader, endpoint); err != nil {
				return
			}
			address = net.JoinHostPort(net.IP(endpoint[:4]).String(), strconv.Itoa(int(binary.BigEndian.Uint16(endpoint[4:]))))
		} else {
			request, err := http.ReadRequest(reader)
			if err != nil || request.Method != http.MethodConnect {
				return
			}
			address = request.Host
		}
		target, err := net.DialTimeout("tcp4", address, time.Second)
		if err != nil {
			return
		}
		defer target.Close()
		_ = target.SetDeadline(time.Now().Add(3 * time.Second))
		if protocol == "socks" {
			_, err = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		} else {
			_, err = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		}
		if err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(target, reader)
			_ = target.(*net.TCPConn).CloseWrite()
			close(done)
		}()
		_, _ = io.Copy(conn, target)
		_ = conn.(*net.TCPConn).CloseWrite()
		<-done
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func tunnelHTTP2EOFProxy(t *testing.T) (int, string) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.ProtoMajor != 2 {
			http.Error(w, "CONNECT required", 400)
			return
		}
		target, err := net.DialTimeout("tcp4", r.Host, time.Second)
		if err != nil {
			http.Error(w, "target unavailable", 502)
			return
		}
		defer target.Close()
		_ = target.SetDeadline(time.Now().Add(3 * time.Second))
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(target, r.Body)
			_ = target.(*net.TCPConn).CloseWrite()
			close(done)
		}()
		_, _ = io.Copy(w, target)
		<-done
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	pin := sha256.Sum256(server.Certificate().Raw)
	return server.Listener.Addr().(*net.TCPAddr).Port, hex.EncodeToString(pin[:])
}

func tunnelSelectProxy(config map[string]any, protocol string, proxyPort int) {
	config["outbounds"] = append(config["outbounds"].([]any), map[string]any{"tag": "selected-proxy", "protocol": protocol, "settings": map[string]any{"address": "127.0.0.1", "port": proxyPort}})
	config["routing"] = map[string]any{"rules": []any{
		map[string]any{"type": "field", "inboundTag": []string{"owned"}, "outboundTag": "selected-proxy"},
		map[string]any{"type": "field", "inboundTag": []string{"proxy-in"}, "outboundTag": "direct"},
	}}
}

func TestTunnelTCPHalfCloseThroughSelectedProxy(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, route := range []string{"socks", "http", "core-socks", "core-http", "http2"} {
			t.Run(fmt.Sprintf("managed-%t/%s", managed, route), func(t *testing.T) {
				target, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = target.Close() })
				received := make(chan []byte, 1)
				go func() {
					conn, err := target.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
					payload, err := io.ReadAll(conn)
					if err == nil {
						received <- payload
						_, _ = conn.Write(payload)
					}
				}()
				listen := port(t)
				config := loopbackTunnelConfig(0, "tcp", false, listen, target.Addr().(*net.TCPAddr).Port)
				if !managed {
					delete(config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any), "clientId")
				}
				protocol := strings.TrimPrefix(route, "core-")
				proxyPort := 0
				var certificatePin string
				switch {
				case strings.HasPrefix(route, "core-"):
					proxyPort = port(t)
					for proxyPort == listen {
						proxyPort = port(t)
					}
					config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "proxy-in", "listen": "127.0.0.1", "port": proxyPort, "protocol": "mixed", "settings": map[string]any{"auth": "noauth"}})
				case route == "http2":
					protocol = "http"
					proxyPort, certificatePin = tunnelHTTP2EOFProxy(t)
				default:
					proxyPort = tunnelEOFProxy(t, protocol)
				}
				tunnelSelectProxy(config, protocol, proxyPort)
				if route == "http2" {
					outbounds := config["outbounds"].([]any)
					outbounds[len(outbounds)-1].(map[string]any)["streamSettings"] = map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"pinnedPeerCertSha256": certificatePin, "alpn": []string{"h2"}}}
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
					t.Fatalf("selected %s lost EOF-dependent reply: n=%d err=%v", route, len(reply), err)
				}
				select {
				case actual := <-received:
					if !bytes.Equal(actual, payload) {
						t.Fatal("target received different upload")
					}
				case <-time.After(time.Second):
					t.Fatal("target did not receive EOF")
				}
				if managed {
					state, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot("owner")
					if err != nil || state.Usage != (clientpolicy.Usage{RawUpload: 9, RawDownload: 9, BilledBytes: 27}) {
						t.Fatalf("proxy half-close billed incorrectly: %+v %v", state, err)
					}
				}
			})
		}
	}
}

func testTunnelSocksDatagram(t *testing.T, upload, reply []byte) {
	t.Helper()
	target, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	received := make(chan []byte, 1)
	go func() {
		data := make([]byte, 65535)
		n, peer, err := target.ReadFrom(data)
		if err == nil {
			received <- append([]byte(nil), data[:n]...)
			_, _ = target.WriteTo(reply, peer)
		}
	}()
	listen, proxyPort := port(t), port(t)
	for listen == target.LocalAddr().(*net.UDPAddr).Port {
		listen = port(t)
	}
	for proxyPort == listen || proxyPort == target.LocalAddr().(*net.UDPAddr).Port {
		proxyPort = port(t)
	}
	config := loopbackTunnelConfig(0, "udp", false, listen, target.LocalAddr().(*net.UDPAddr).Port)
	config["inbounds"] = append(config["inbounds"].([]any), map[string]any{"tag": "proxy-in", "listen": "127.0.0.1", "port": proxyPort, "protocol": "mixed", "settings": map[string]any{"auth": "noauth", "udp": true, "udpFullDatagrams": true}})
	tunnelSelectProxy(config, "socks", proxyPort)
	instance := start(t, loopbackJSON(t, config))
	flow := loopbackFlow(t, "udp", listen)
	if _, err := flow.Write(upload); err != nil {
		t.Fatal(err)
	}
	select {
	case actual := <-received:
		if !bytes.Equal(actual, upload) {
			t.Fatalf("SOCKS target payload: got=%d want=%d", len(actual), len(upload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SOCKS target received no datagram")
	}
	_ = flow.SetReadDeadline(time.Now().Add(2 * time.Second))
	data := make([]byte, 65535)
	if n, err := flow.Read(data); err != nil || !bytes.Equal(data[:n], reply) {
		t.Fatalf("SOCKS Tunnel reply: n=%d want=%d err=%v", n, len(reply), err)
	}
	state, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot("owner")
	want := clientpolicy.Usage{RawUpload: uint64(len(upload)), RawDownload: uint64(len(reply)), BilledBytes: uint64((len(upload) + len(reply)) * 3 / 2), Remainder: uint64((len(upload) + len(reply)) % 2 * 500000)}
	if err != nil || state.Usage != want {
		t.Fatalf("SOCKS payload billing: %+v want=%+v %v", state, want, err)
	}
}

func TestTunnelSelectedSocksLargeUploadPreservesTargetAndLedger(t *testing.T) {
	for _, size := range []int{13000, 65497} {
		t.Run(strconv.Itoa(size), func(t *testing.T) { testTunnelSocksDatagram(t, bytes.Repeat([]byte{0x83}, size), []byte("ok")) })
	}
}

func TestTunnelSelectedSocksLargeReplyPreservesTargetAndLedger(t *testing.T) {
	for _, size := range []int{13000, 65497} {
		t.Run(strconv.Itoa(size), func(t *testing.T) { testTunnelSocksDatagram(t, []byte("go"), bytes.Repeat([]byte{0x83}, size)) })
	}
}

func TestTunnelSelectedSocksEmptyDatagramPreservesTargetAndLedger(t *testing.T) {
	testTunnelSocksDatagram(t, []byte{}, []byte{})
}
