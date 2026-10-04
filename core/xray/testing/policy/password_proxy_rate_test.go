package policy_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/testing/testauthority"
)

func TestPasswordMixedAliasesShareDirectionalRates(t *testing.T) {
	for _, direction := range []string{"upload", "download"} {
		t.Run(direction, func(t *testing.T) {
			target, received := loopbackEchoTarget(t, "tcp")
			authPort, tunnelPort := port(t), port(t)
			config := passwordProxyConfig(t, "mixed", authPort, tunnelPort, target)
			settings := config["inbounds"].([]any)[1].(map[string]any)["settings"].(map[string]any)
			settings["accounts"] = append(settings["accounts"].([]any), map[string]any{"user": "http-alias", "pass": "alias-password", "clientId": "owner", "email": "canonical-account"})
			instance := start(t, loopbackJSON(t, config))
			first, err := passwordProxyDial("socks", authPort, target, "authenticated-user", "test-password")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { first.Close() })
			alias, err := passwordProxyDial("http", authPort, target, "http-alias", "alias-password")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { alias.Close() })
			tunnel := loopbackFlow(t, "tcp", tunnelPort)
			for _, conn := range []net.Conn{first, alias, tunnel} {
				exchange(t, conn, []byte("start!"))
			}
			engine := instance.GetFeature((*clientpolicy.Manager)(nil)).(*clientpolicy.Engine)
			policy, _, err := engine.GetClient("owner")
			if err != nil {
				t.Fatal(err)
			}
			policy.Version++
			if direction == "upload" {
				policy.UploadRate = 1
			} else {
				policy.DownloadRate = 1
			}
			if err := testauthority.Apply(t, engine, policy); err != nil {
				t.Fatal(err)
			}
			exchange(t, first, bytes.Repeat([]byte{'b'}, 65536))
			completed := make(chan error, 2)
			for _, conn := range []net.Conn{alias, tunnel} {
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				go func() {
					payload := []byte("queued")
					if _, err := conn.Write(payload); err != nil {
						completed <- err
						return
					}
					reply := make([]byte, len(payload))
					_, err := io.ReadFull(conn, reply)
					if err == nil && !bytes.Equal(payload, reply) {
						err = fmt.Errorf("queued reply differs: %q", reply)
					}
					completed <- err
				}()
			}
			select {
			case err := <-completed:
				t.Fatalf("%s did not share SOCKS's exhausted bucket: %v", direction, err)
			case <-time.After(200 * time.Millisecond):
			}
			// For download limiting, observe both uploads before changing the rate
			// so their original multiplier is independently established.
			if direction == "download" {
				deadline := time.Now().Add(time.Second)
				for received.Load() < 65566 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if received.Load() != 65566 {
					t.Fatalf("queued download uploads not observed: %d", received.Load())
				}
			}
			policy.Version++
			policy.UploadRate, policy.DownloadRate = 0, 0
			policy.Multiplier = 500000
			if err := testauthority.Apply(t, engine, policy); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				select {
				case err := <-completed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("hot rate update did not release queued streams")
				}
			}
			billed := uint64(196674)
			if direction == "download" {
				billed = 196686
			}
			snapshot, err := engine.Snapshot("owner")
			if err != nil || snapshot.Usage != (clientpolicy.Usage{RawUpload: 65566, RawDownload: 65566, BilledBytes: billed}) || received.Load() != 65566 {
				t.Fatalf("shared rate/multiplier rewrote history: %+v err=%v target=%d", snapshot, err, received.Load())
			}
			manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
			handler, err := manager.GetHandler(context.Background(), "password")
			if err != nil {
				t.Fatal(err)
			}
			if err := (&command.RemoveUserOperation{Email: "canonical-account"}).ApplyInbound(context.Background(), handler); err != nil {
				t.Fatal(err)
			}
			assertPasswordProxyClosed(t, first)
			assertPasswordProxyClosed(t, alias)
			if handler.(proxy.GetInbound).GetInbound().(proxy.UserManager).GetUsersCount(context.Background()) != 0 {
				t.Fatal("canonical removal retained a password alias")
			}
			tunnel.SetDeadline(time.Time{})
			exchange(t, tunnel, []byte("alive!"))
		})
	}
}
