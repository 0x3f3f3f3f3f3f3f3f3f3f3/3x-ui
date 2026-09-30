package policy_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
)

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
	}{
		{"selected", "selected", "block", true},
		{"block", "block", "selected", false},
		{"removed", "removed", "selected", false},
		{"inherit-selected", "", "selected", true},
		{"inherit-block", "", "block", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			innerPort, innerReceived := loopbackEchoTarget(t, "tcp")
			selectedPort, selectedReceived := loopbackEchoTarget(t, "tcp")
			listen := port(t)
			config := loopbackTunnelConfig(0, "tcp", false, listen, innerPort)
			settings := config["inbounds"].([]any)[0].(map[string]any)["settings"].(map[string]any)
			settings["rewriteAddress"], settings["outboundTag"] = "v1.mux.cool", tc.fixed
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
			if tc.allow {
				exchange(t, flow, payload)
				want = clientpolicy.Usage{RawUpload: 14, RawDownload: 14, BilledBytes: 42}
			} else {
				_ = flow.SetDeadline(time.Now().Add(time.Second))
				_, _ = flow.Write(payload)
				var timeout net.Error
				if _, err := flow.Read(make([]byte, 32)); err == nil || errors.As(err, &timeout) && timeout.Timeout() {
					t.Fatalf("selection %s failed to reject mux payload: %v", tc.name, err)
				}
			}
			snapshot, err := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine).Snapshot("owner")
			if err != nil || snapshot.Usage != want || innerReceived.Load() != 0 || selectedReceived.Load() != int64(want.RawUpload) {
				t.Fatalf("mux payload escaped fixed selection: usage=%+v err=%v inner=%d selected=%d", snapshot.Usage, err, innerReceived.Load(), selectedReceived.Load())
			}
		})
	}
}
