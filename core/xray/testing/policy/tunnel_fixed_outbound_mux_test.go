package policy_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
)

func TestLegacyUnownedTunnelMuxStillDispatches(t *testing.T) {
	if os.Getenv("XRAY_LEGACY_TUNNEL_MUX_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestLegacyUnownedTunnelMuxStillDispatches$", "-test.v")
		cmd.Env = append(os.Environ(), "XRAY_LEGACY_TUNNEL_MUX_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("legacy Tunnel mux subprocess failed: %v\n%s", err, output)
		}
		return
	}
	innerPort, innerReceived := loopbackEchoTarget(t, "tcp")
	listen := port(t)
	config := loopbackTunnelConfig(0, "tcp", false, listen, innerPort)
	settings := config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
	delete(settings, "clientId")
	delete(settings, "email")
	settings["rewriteAddress"] = "v1.mux.cool"
	instance := start(t, loopbackJSON(t, config))
	flow := loopbackFlow(t, "tcp", listen)
	if err := flow.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// A legacy, unowned Tunnel retains the upstream internal mux gateway.
	request := []byte{0, 12, 0, 1, 1, 1, 1, byte(innerPort >> 8), byte(innerPort), 1, 127, 0, 0, 1, 0, 6, 'l', 'e', 'g', 'a', 'c', 'y'}
	if _, err := flow.Write(request); err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 4, 0, 1, 2, 1, 0, 6, 'l', 'e', 'g', 'a', 'c', 'y'}
	response := make([]byte, len(want))
	if _, err := io.ReadFull(flow, response); err != nil {
		t.Fatalf("legacy mux failed to reach its inner target: %v", err)
	}
	snapshot, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot("owner")
	if !bytes.Equal(response, want) || innerReceived.Load() != 6 || err != nil || snapshot.Usage != (clientpolicy.Usage{}) {
		t.Fatalf("legacy mux response=%x want=%x inner=%d unrelated usage=%+v err=%v", response, want, innerReceived.Load(), snapshot.Usage, err)
	}
}

func TestTunnelFixedOutboundTreatsMuxDestinationAsPayload(t *testing.T) {
	if os.Getenv("XRAY_FIXED_TUNNEL_MUX_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestTunnelFixedOutboundTreatsMuxDestinationAsPayload$", "-test.v")
		cmd.Env = append(os.Environ(), "XRAY_FIXED_TUNNEL_MUX_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixed Tunnel mux destination subprocess failed: %v\n%s", err, output)
		}
		return
	}
	for _, tc := range []struct {
		name, fixed, routed string
		allow               bool
		unowned             bool
	}{
		{"selected", "selected", "block", true, false},
		{"block", "block", "selected", false, false},
		{"removed", "removed", "selected", false, false},
		{"inherit-selected", "", "selected", true, false},
		{"inherit-block", "", "block", false, false},
		{"unowned-selected", "selected", "block", true, true},
		{"unowned-block", "block", "selected", false, true},
		{"unowned-removed", "removed", "selected", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			innerPort, innerReceived := loopbackEchoTarget(t, "tcp")
			selectedPort, selectedReceived := loopbackEchoTarget(t, "tcp")
			listen := port(t)
			config := loopbackTunnelConfig(0, "tcp", false, listen, innerPort)
			settings := config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
			settings["rewriteAddress"], settings["outboundTag"] = "v1.mux.cool", tc.fixed
			if tc.unowned {
				delete(settings, "clientId")
				delete(settings, "email")
			}
			config["routing"] = map[string]any{"rules": []any{
				map[string]any{"type": "field", "inboundTag": []string{"owned"}, "outboundTag": tc.routed},
			}}
			config["outbounds"] = append(config["outbounds"].([]any), map[string]any{
				"tag": "selected", "protocol": "freedom", "settings": map[string]any{
					"redirect":   fmt.Sprintf("127.0.0.1:%d", selectedPort),
					"finalRules": []any{map[string]any{"action": "allow"}},
				},
			})
			instance := start(t, loopbackJSON(t, config))
			flow := loopbackFlow(t, "tcp", listen)
			// A valid New frame must remain opaque payload for the selected outbound.
			payload := []byte{0, 12, 0, 1, 1, 0, 1, byte(innerPort >> 8), byte(innerPort), 1, 127, 0, 0, 1}
			want := clientpolicy.Usage{}
			var wantSelected int64
			if tc.allow {
				exchange(t, flow, payload)
				wantSelected = 14
				if !tc.unowned {
					want = clientpolicy.Usage{RawUpload: 14, RawDownload: 14, BilledBytes: 42}
				}
			} else {
				_ = flow.SetDeadline(time.Now().Add(time.Second))
				_, _ = flow.Write(payload)
				var timeout net.Error
				if _, err := flow.Read(make([]byte, 32)); err == nil || errors.As(err, &timeout) && timeout.Timeout() {
					t.Fatalf("selection %s failed to reject mux payload: %v", tc.name, err)
				}
			}
			snapshot, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot("owner")
			if err != nil || snapshot.Usage != want || innerReceived.Load() != 0 || selectedReceived.Load() != wantSelected {
				t.Fatalf("mux payload escaped fixed selection: usage=%+v err=%v inner=%d selected=%d", snapshot.Usage, err, innerReceived.Load(), selectedReceived.Load())
			}
		})
	}
}
