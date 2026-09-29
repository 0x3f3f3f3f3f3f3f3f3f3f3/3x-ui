package mieru

import (
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestNativePresenceTracksOnlyAdmittedLiveFlows(t *testing.T) {
	for _, underlay := range []string{"tcp", "udp"} {
		t.Run(underlay, func(t *testing.T) {
			db, ledger, controller := mieruDB(t)
			_, user := mieruUser(t, db, ledger, controller, 1000)
			rejected, denied := mieruUser(t, db, ledger, controller, 1000)
			if err := db.Model(&model.ClientRecord{}).Where("id = ?", rejected.Id).Update("enable", false).Error; err != nil {
				t.Fatal(err)
			}
			server := startNative(t, controller, underlay, user, denied)
			tcpTarget, udpTarget := nativeEcho(t, "tcp"), nativeEcho(t, "udp")
			pending := rawNativeSession(t, clientMux(t, server.Addresses()[0], user))
			if _, err := pending.Write([]byte{5}); err != nil {
				t.Fatal(err)
			}
			requireDeniedSession(t, officialClient(t, server.Addresses()[0], denied), tcpTarget)
			if online := server.OnlineSessions(); len(online) != 0 {
				t.Fatalf("pending or rejected request appeared online: %+v", online)
			}
			client := officialClient(t, server.Addresses()[0], user)
			tcp := wireEcho(t, client, tcpTarget, []byte("online-tcp"))
			udp := wireEcho(t, client, udpTarget, []byte("online-udp"))
			assertPresence := func(want int) {
				t.Helper()
				online := server.OnlineSessions()
				if len(online) != want {
					t.Fatalf("admitted live flows=%d, want %d", len(online), want)
				}
				for _, session := range online {
					if session.PolicyID != user.PolicyID || session.Username != user.Username || session.SourceIP.String() != "127.0.0.1" {
						t.Fatalf("online source lost authenticated identity or actual peer: %+v", session)
					}
				}
				status := server.Status()
				if !status.Listening || status.AuthenticatedSessions != want {
					t.Fatalf("runtime status disagrees with admitted flows: %+v", status)
				}
			}
			assertPresence(2)
			if err := tcp.Close(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) && len(server.OnlineSessions()) != 1 {
				time.Sleep(time.Millisecond)
			}
			assertPresence(1)
			rotated := user
			rotated.Password = "presence-rotation-password"
			if err := server.UpdateClients([]Client{rotated, denied}); err != nil {
				t.Fatal(err)
			}
			assertPresence(0)
			requireClosedWire(t, udp)
			if err := server.Close(); err != nil {
				t.Fatal(err)
			}
			if status := server.Status(); status.Listening || status.AuthenticatedSessions != 0 {
				t.Fatalf("stopped listener remained online: %+v", status)
			}
		})
	}
}
