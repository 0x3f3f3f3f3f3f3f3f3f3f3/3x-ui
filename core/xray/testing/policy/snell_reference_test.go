package policy_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	coreSnell "github.com/xtls/xray-core/proxy/snell"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/stat"
)

// Reference programs are opt-in, test-only interoperability fixtures. Product
// handlers never launch a server or use a fixture as a runtime fallback.
func snellReferenceServer(t *testing.T, version int, mode, obfs, psk string) int {
	t.Helper()
	root := os.Getenv("SNELL_REFERENCE_DIR")
	if root == "" {
		t.Skip("set SNELL_REFERENCE_DIR to pinned official linux-aarch64 fixtures")
	}
	var tag, want string
	switch version {
	case 4:
		tag = "v4.1.1"
		want = "a6dceb898ade6da58840bf26499a0747894fb1c6407878139c8d863e7926d297"
	case 5:
		tag = "v5.0.1"
		want = "c9e1cc1f1a86e7d2958f2bc41ff9dc668edf479455a651ea05c6db2c18cd2e4e"
	case 6:
		tag = "v6.0.0rc2"
		want = "316c924cb2f7bea75278303265cf004c66379244e101c64ab672a1c987bf8041"
	}
	path := filepath.Join(root, tag, "snell-server")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != want {
		t.Fatal("official reference binary hash does not match pinned fixture")
	}
	listen := port(t)
	config := fmt.Sprintf("[snell-server]\nlisten = 127.0.0.1:%d\npsk = %s\nipv6 = true\n", listen, psk)
	if version == 6 {
		config += "mode = " + mode + "\n"
	} else if obfs != "" {
		config += "obfs = " + obfs + "\n"
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "snell.conf")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	cmd := exec.Command(path, "-c", configPath)
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", listen), 50*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("official reference server did not open its listener")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("official fixture %s SHA256 %s", tag, want)
	return listen
}

func TestNativeSnellOfficialServerInterop(t *testing.T) {
	for _, tc := range []struct {
		version    int
		mode, obfs string
	}{{4, "", ""}, {4, "", "http"}, {5, "", ""}, {5, "", "http"}, {6, "default", ""}, {6, "unshaped", ""}} {
		for _, network := range []string{"tcp", "udp"} {
			for _, reuse := range []bool{false, true} {
				if network == "udp" && reuse {
					continue
				}
				t.Run(fmt.Sprintf("v%d/%s/%s/%s/reuse=%t", tc.version, tc.mode, tc.obfs, network, reuse), func(t *testing.T) {
					server := snellReferenceServer(t, tc.version, tc.mode, tc.obfs, snellPSK)
					target, received := loopbackEchoTarget(t, network)
					listen := port(t)
					config := snellNativeConfig(tc.version, port(t))
					config["inbounds"] = []any{map[string]any{"tag": "origin", "listen": "127.0.0.1", "port": listen, "protocol": "tunnel", "settings": map[string]any{"allowedNetwork": network, "rewriteAddress": "127.0.0.1", "rewritePort": target, "clientId": snellOwner, "email": "reference-owner"}}}
					config["outbounds"] = []any{map[string]any{"protocol": "snell", "settings": map[string]any{"version": tc.version, "psk": snellPSK, "address": "127.0.0.1", "port": server, "mode": tc.mode, "obfs": tc.obfs, "obfsHost": "example.test", "reuse": reuse}}}
					instance := start(t, loopbackJSON(t, config))
					flow := loopbackFlow(t, network, listen)
					payload := bytes.Repeat([]byte{0x31}, 768)
					exchange(t, flow, payload)
					snap, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot(snellOwner)
					if err != nil || snap.Usage != (clientpolicy.Usage{RawUpload: 768, RawDownload: 768, BilledBytes: 2304}) || received.Load() != 768 {
						t.Fatalf("official-server transfer/accounting: %+v err=%v received=%d", snap, err, received.Load())
					}
				})
			}
		}
	}
}

type referenceDialer struct{}

func (referenceDialer) Dial(ctx context.Context, dest X.Destination) (stat.Connection, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", dest.NetAddr())
}
func (referenceDialer) DestIpAddress() X.IP                                   { return nil }
func (referenceDialer) SetOutboundGateway(context.Context, *session.Outbound) {}

type referencePacketReader struct {
	ctx     context.Context
	cancel  context.CancelFunc
	packets chan buf.MultiBuffer
}

func (r *referencePacketReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	select {
	case p := <-r.packets:
		return p, nil
	case <-r.ctx.Done():
		return nil, r.ctx.Err()
	}
}
func (r *referencePacketReader) Interrupt() { r.cancel() }

type referencePacketWriter struct {
	ctx     context.Context
	cancel  context.CancelFunc
	packets chan buf.MultiBuffer
}

func (w *referencePacketWriter) WriteMultiBuffer(p buf.MultiBuffer) error {
	select {
	case w.packets <- p:
		return nil
	case <-w.ctx.Done():
		buf.ReleaseMulti(p)
		return w.ctx.Err()
	}
}
func (w *referencePacketWriter) Interrupt() { w.cancel() }

func TestNativeSnellOfficialFirstLargeDatagramUpload(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			server := snellReferenceServer(t, version, "default", "", snellPSK)
			echo, received := snellPacketEcho(t, "127.0.0.1")
			out, err := coreSnell.NewClient(context.Background(), &coreSnell.ClientConfig{Version: uint32(version), Psk: snellPSK, Address: X.NewIPOrDomain(X.LocalHostIP), Port: uint32(server)})
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			target := X.UDPDestination(X.LocalHostIP, X.Port(echo.LocalAddr().(*net.UDPAddr).Port))
			ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target}})
			reader := &referencePacketReader{ctx: ctx, cancel: cancel, packets: make(chan buf.MultiBuffer, 1)}
			writer := &referencePacketWriter{ctx: ctx, cancel: cancel, packets: make(chan buf.MultiBuffer, 8)}
			finished := make(chan error, 1)
			go func() {
				finished <- out.Process(ctx, &transport.Link{Reader: reader, Writer: writer}, referenceDialer{})
			}()
			defer func() {
				cancel()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Error("native reference probe did not release its copy goroutines")
				}
				for len(writer.packets) > 0 {
					buf.ReleaseMulti(<-writer.packets)
				}
			}()
			payload := bytes.Repeat([]byte{0x51}, 13000)
			packet := buf.NewWithSize(int32(len(payload)))
			packet.Write(payload)
			packet.UDP = &target
			reader.packets <- buf.MultiBuffer{packet}
			deadline := time.Now().Add(2 * time.Second)
			for received.Load() != 13000 {
				if time.Now().After(deadline) {
					t.Fatalf("official server did not receive first complete datagram: target=%d", received.Load())
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case reply := <-writer.packets:
				t.Logf("first datagram upload=13000; official reply record=%d bytes (separate fixture behavior)", reply.Len())
				if version == 6 && (len(reply) != 1 || !bytes.Equal(reply[0].Bytes(), payload)) {
					buf.ReleaseMulti(reply)
					t.Fatal("official v6 changed first large datagram reply")
				}
				buf.ReleaseMulti(reply)
			case <-time.After(time.Second):
				if version == 6 {
					t.Fatal("official v6 did not return the complete first large datagram reply")
				}
				t.Log("official fixture returned no complete first large reply; upload acceptance is verified separately")
			}
		})
	}
}
