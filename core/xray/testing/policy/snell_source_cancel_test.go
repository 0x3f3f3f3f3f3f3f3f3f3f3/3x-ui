package policy_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	netproxy "golang.org/x/net/proxy"
)

func TestNativeSnellOutboundSniffingIdleCancellation(t *testing.T) {
	for _, source := range []string{"socks", "http"} {
		for _, version := range []int{4, 5, 6} {
			for _, reuse := range []bool{false, true} {
				for _, sniffing := range []bool{false, true} {
					name := fmt.Sprintf("%s/v%d/reuse-%t/sniff-%t", source, version, reuse, sniffing)
					t.Run(name, func(t *testing.T) {
						target, err := net.Listen("tcp4", "127.0.0.1:0")
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = target.Close() })
						received := make(chan struct{}, 1)
						go func() {
							conn, err := target.Accept()
							if err != nil {
								return
							}
							defer conn.Close()
							var payload [1]byte
							if _, err := io.ReadFull(conn, payload[:]); err == nil && payload[0] == 0x71 {
								received <- struct{}{}
							}
							_, _ = io.Copy(io.Discard, conn)
						}()
						serverPort, socksPort := port(t), port(t)
						start(t, loopbackJSON(t, snellNativeConfig(version, serverPort)))
						client := map[string]any{
							"log":    map[string]any{"loglevel": "error"},
							"policy": map[string]any{"levels": map[string]any{"0": map[string]any{"connIdle": 1, "uplinkOnly": 1, "downlinkOnly": 1}}},
							"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": source, "settings": map[string]any{"auth": "noauth"},
								"sniffing": map[string]any{"enabled": sniffing, "metadataOnly": true}}},
							"outbounds": []any{map[string]any{"protocol": "snell", "settings": map[string]any{"version": version, "address": "127.0.0.1", "port": serverPort, "psk": snellPSK, "reuse": reuse}}},
						}
						start(t, loopbackJSON(t, client))
						var conn net.Conn
						if source == "socks" {
							dialer, err := netproxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), nil, &net.Dialer{Timeout: time.Second})
							if err != nil {
								t.Fatal(err)
							}
							conn, err = dialer.Dial("tcp", target.Addr().String())
						} else {
							conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", socksPort), time.Second)
							if err == nil {
								_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target.Addr(), target.Addr())
								if err == nil {
									response, responseErr := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
									err = responseErr
									if err == nil && response.StatusCode != http.StatusOK {
										err = fmt.Errorf("HTTP CONNECT status %d", response.StatusCode)
									}
								}
							}
						}
						if err != nil {
							if conn != nil {
								_ = conn.Close()
							}
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = conn.Close() })
						if _, err := conn.Write([]byte{0x71}); err != nil {
							t.Fatal(err)
						}
						select {
						case <-received:
						case <-time.After(2 * time.Second):
							t.Fatal("socket source/Snell did not deliver the initial payload to the silent target")
						}
						_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
						var reply [1]byte
						if _, err := conn.Read(reply[:]); err == nil {
							t.Fatal("silent target unexpectedly replied")
						} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
							t.Fatal("idle cancellation retained the input source socket and blocked copy")
						}
					})
				}
			}
		}
	}
}
