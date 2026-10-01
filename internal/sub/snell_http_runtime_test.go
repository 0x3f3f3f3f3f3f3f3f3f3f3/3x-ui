package sub

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	netproxy "golang.org/x/net/proxy"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type snellHTTPClient struct {
	port int
	done chan struct{}
	log  string
	stop context.CancelFunc
}

func (h *sshHTTPHarness) snellClient(t *testing.T, subID string) *snellHTTPClient {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sub/"+subID+"?format=snell-json", nil)
	req.Host = "127.0.0.1"
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("public native Snell download: %d %s", w.Code, w.Body.String())
	}
	var config map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	port := sshHTTPPort(t)
	// Change only the local SOCKS port, preserving the downloaded native outbound.
	config["inbounds"].([]any)[0].(map[string]any)["port"] = port
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "downloaded-client.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "client.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, os.Getenv("XRAY_E2E_BINARY"), "run", "-c", path)
	cmd.Stdout, cmd.Stderr = log, log
	client := &snellHTTPClient{port: port, done: make(chan struct{}), log: logPath, stop: cancel}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = log.Close()
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait(); _ = log.Close(); close(client.done) }()
	t.Cleanup(func() { cancel(); <-client.done })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-client.done:
			output, _ := os.ReadFile(logPath)
			t.Fatalf("downloaded native Snell client stopped: %s", output)
		default:
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return client
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("downloaded native Snell client did not open SOCKS listener")
	return nil
}

func (c *snellHTTPClient) tcp(t *testing.T, target net.Addr) net.Conn {
	t.Helper()
	dialer, err := netproxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", c.port), nil, &net.Dialer{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("tcp", target.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (c *snellHTTPClient) udp(t *testing.T) net.Conn {
	t.Helper()
	control, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", c.port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	_ = control.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := control.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	var auth [2]byte
	if _, err := io.ReadFull(control, auth[:]); err != nil || auth != [2]byte{5, 0} {
		t.Fatalf("SOCKS authentication: %v %v", auth, err)
	}
	if _, err := control.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(control, reply[:]); err != nil || reply[1] != 0 || reply[3] != 1 {
		t.Fatalf("SOCKS UDP associate: %v %v", reply, err)
	}
	_ = control.SetDeadline(time.Time{})
	relay := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(binary.BigEndian.Uint16(reply[8:]))}
	packet, err := net.DialUDP("udp4", nil, relay)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packet.Close() })
	return packet
}

func snellHTTPSocksPacket(t *testing.T, packet net.Conn, target net.Addr, payload []byte) {
	t.Helper()
	destination := target.(*net.UDPAddr)
	header := []byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(header[8:], uint16(destination.Port))
	_ = packet.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := packet.Write(append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 65535)
	n, err := packet.Read(reply)
	if err != nil || n < 10 || !bytes.Equal(reply[:10], header) || !bytes.Equal(reply[10:n], payload) {
		t.Fatalf("downloaded native Snell UDP payload/source: length=%d want=%d err=%v", n, len(payload)+10, err)
	}
	_ = packet.SetDeadline(time.Time{})
}

func TestSnellHTTPExportRealCoreTCPUDPQUICAndSharedLifecycle(t *testing.T) {
	for _, transport := range []struct {
		version int
		quic    bool
	}{{4, false}, {5, false}, {5, true}, {6, false}} {
		name := fmt.Sprintf("v%d-quic-%t", transport.version, transport.quic)
		t.Run(name, func(t *testing.T) {
			h := newNativeHTTPHarness(t, "snell_http")
			tunnel := h.add(t, "tunnel", "shared-tunnel", fmt.Sprintf(`{"address":"127.0.0.1","port":%d,"network":"tcp","clients":[]}`, h.target.Addr().(*net.TCPAddr).Port))
			a := model.Client{Email: "http-snell-owner", SubID: "http-snell-sub", Enable: true, TotalGB: 1000000, SnellPSK: `原生,#"\\psk-独立`, Policy: &model.ClientPolicyOptions{Multiplier: "2"}}
			h.api(t, http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: a, InboundIds: []int{tunnel.Id}})
			var owner model.ClientRecord
			if err := database.GetDB().Where("email = ?", a.Email).First(&owner).Error; err != nil {
				t.Fatal(err)
			}
			addSnell := func(tag, ownerID string, quic bool) model.Inbound {
				t.Helper()
				settings, _ := json.Marshal(map[string]any{"version": transport.version, "quic": quic, "clients": []any{}})
				raw := h.api(t, http.MethodPost, "/panel/api/inbounds/add", map[string]any{"protocol": "snell", "tag": tag, "listen": "127.0.0.1", "port": sshHTTPPort(t), "enable": true, "ownerClientId": ownerID, "settings": string(settings), "streamSettings": "{}"})
				var inbound model.Inbound
				if err := json.Unmarshal(raw, &inbound); err != nil {
					t.Fatal(err)
				}
				return inbound
			}
			listener := addSnell("native-snell", owner.StableID, transport.quic)
			b := model.Client{Email: "http-snell-sibling", SubID: "http-snell-sibling-sub", Enable: true, SnellPSK: "sibling-native-secret"}
			h.api(t, http.MethodPost, "/panel/api/clients/add", service.ClientCreatePayload{Client: b})
			var sibling model.ClientRecord
			if err := database.GetDB().Where("email = ?", b.Email).First(&sibling).Error; err != nil {
				t.Fatal(err)
			}
			addSnell("native-sibling", sibling.StableID, false)
			if err := h.svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			client := h.snellClient(t, a.SubID)
			first := client.tcp(t, h.target.Addr())
			other := h.snellClient(t, b.SubID).tcp(t, h.target.Addr())
			shared, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tunnel.Port), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = shared.Close() })
			packetTarget, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = packetTarget.Close() })
			go func() {
				buf := make([]byte, 65535)
				for {
					n, addr, err := packetTarget.ReadFrom(buf)
					if err != nil {
						return
					}
					_, _ = packetTarget.WriteTo(buf[:n], addr)
				}
			}()
			packets := client.udp(t)
			var expected int64
			echo := func(flow net.Conn, payload string) {
				t.Helper()
				sshHTTPEcho(t, flow, payload)
				expected += int64(len(payload))
			}
			checkLedger := func() {
				t.Helper()
				if _, _, err := h.svc.GetXrayTraffic(); err != nil {
					t.Fatal(err)
				}
				var total model.ClientPolicyTotal
				if err := database.GetDB().Where("client_id = ?", owner.StableID).First(&total).Error; err != nil {
					t.Fatal(err)
				}
				if total.RawUpload != expected || total.RawDownload != expected || total.BilledBytes != expected*4 {
					t.Fatalf("HTTP download Snell/Tunnel exact shared ledger: %+v expected=%d", total, expected)
				}
			}
			echo(first, "native")
			echo(shared, "shared")
			sshHTTPEcho(t, other, "sibling")
			for _, payload := range [][]byte{bytes.Repeat([]byte{0x71}, 13000), {}} {
				snellHTTPSocksPacket(t, packets, packetTarget.LocalAddr(), payload)
				expected += int64(len(payload))
			}
			checkLedger()
			a.SnellPSK = "rotated-native-secret"
			h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			var rotated model.ClientRecord
			if err := database.GetDB().First(&rotated, owner.Id).Error; err != nil || rotated.SnellPSK != a.SnellPSK {
				t.Fatalf("public PSK update did not reach canonical SQL record: %v", err)
			}
			sshHTTPClosed(t, first)
			stale := client.tcp(t, h.target.Addr())
			_ = stale.SetDeadline(time.Now().Add(time.Second))
			_, _ = stale.Write([]byte("denied"))
			if _, err := stale.Read(make([]byte, 1)); err == nil {
				t.Fatal("old downloaded native PSK remained usable")
			}
			echo(shared, "alive")
			sshHTTPEcho(t, other, "after-rotation")
			current := h.snellClient(t, a.SubID).tcp(t, h.target.Addr())
			echo(current, "rotated")
			for _, direction := range []string{"upload", "download"} {
				if direction == "upload" {
					a.Policy.UploadBytesPerSecond = 1
				} else {
					a.Policy.DownloadBytesPerSecond = 1
				}
				h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
				echo(current, strings.Repeat("x", 65536))
				done := make(chan error, 1)
				go func() {
					_, err := io.WriteString(shared, "queued")
					if err == nil {
						reply := make([]byte, 6)
						_, err = io.ReadFull(shared, reply)
						if err == nil && string(reply) != "queued" {
							err = fmt.Errorf("queued response differs")
						}
					}
					done <- err
				}()
				select {
				case err := <-done:
					t.Fatalf("public %s policy failed to share Snell/Tunnel rate: %v", direction, err)
				case <-time.After(200 * time.Millisecond):
				}
				a.Policy.UploadBytesPerSecond, a.Policy.DownloadBytesPerSecond = 0, 0
				h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("rate renewal did not release shared Tunnel")
				}
				expected += 6
			}
			checkLedger()
			a.ExpiryTime = time.Now().Add(750 * time.Millisecond).UnixMilli()
			h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			sshHTTPClosed(t, current)
			sshHTTPClosed(t, shared)
			sshHTTPEcho(t, other, "after-expiry")
			a.ExpiryTime = 0
			h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			current = h.snellClient(t, a.SubID).tcp(t, h.target.Addr())
			echo(current, "renewed")
			a.Enable = false
			h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			sshHTTPClosed(t, current)
			sshHTTPEcho(t, other, "after-disable")
			a.Enable = true
			h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			current = h.snellClient(t, a.SubID).tcp(t, h.target.Addr())
			echo(current, "enabled")
			a.TotalGB = 1
			h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			sshHTTPClosed(t, current)
			sshHTTPEcho(t, other, "after-quota")
			a.TotalGB = 1000000
			h.api(t, http.MethodPost, "/panel/api/clients/update/"+a.Email, a)
			current = h.snellClient(t, a.SubID).tcp(t, h.target.Addr())
			echo(current, "quota-renewed")
			checkLedger()
			h.api(t, http.MethodPost, "/panel/api/clients/"+a.Email+"/detach", map[string]any{"inboundIds": []int{listener.Id}})
			sshHTTPClosed(t, current)
			sshHTTPEcho(t, other, "after-detach")
			var saved model.Inbound
			if err := database.GetDB().First(&saved, listener.Id).Error; err != nil || saved.Enable {
				t.Fatalf("last detach retained active SQL listener: %v", err)
			}
			address := fmt.Sprintf("127.0.0.1:%d", listener.Port)
			released, err := net.Listen("tcp4", address)
			if err != nil {
				t.Fatal("last detach retained TCP port", err)
			}
			_ = released.Close()
			if transport.version == 5 {
				udp, err := net.ListenPacket("udp4", address)
				if err != nil {
					t.Fatal("last detach retained QUIC/UDP port", err)
				}
				_ = udp.Close()
			}
			if err := h.svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			sshHTTPEcho(t, h.snellClient(t, b.SubID).tcp(t, h.target.Addr()), "restart")
			if err := database.GetDB().First(&owner, owner.Id).Error; err != nil || owner.SnellPSK != a.SnellPSK {
				t.Fatal("last detach/restart lost canonical Snell credential", err)
			}
			checkLedger()
		})
	}
}
