package protocol

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/enfein/mieru/v3/pkg/cipher"
	"github.com/enfein/mieru/v3/pkg/common"
)

func TestManagedPacketIdleExpiryDoesNotNeedAnotherValidPacket(t *testing.T) {
	for _, noise := range []bool{false, true} {
		name := "quiet"
		if noise {
			name = "invalid-packets"
		}
		t.Run(name, func(t *testing.T) {
			const user = "idle-user"
			credential := cipher.HashPassword([]byte(t.Name()), []byte(user))
			mux := NewMux(false).SetServerUsers(userMap(makeTestUser(user, credential)))
			t.Cleanup(func() { _ = mux.Close() })
			if err := mux.SetServerLimits(ServerLimits{Sessions: 4, SessionsPerUser: 4, QueueSegments: 64, QueueBytes: 128 << 10, DisableUserMetrics: true}); err != nil {
				t.Fatal(err)
			}
			conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := &PacketUnderlay{baseUnderlay: *mux.newServerUnderlay(1400, nil), conn: conn, sessionCleanTicker: time.NewTicker(sessionCleanInterval), serverUsers: &mux.serverUsers}
			loop := make(chan error, 1)
			go func() { loop <- server.RunEventLoop(t.Context()) }()
			t.Cleanup(func() {
				_ = server.Close()
				_ = conn.Close()
				select {
				case <-loop:
				case <-time.After(time.Second):
					t.Error("packet loop did not terminate")
				}
			})
			sender := newServerUserPacketSender(t, conn.LocalAddr(), credential, user)
			t.Cleanup(func() { _ = sender.conn.Close() })
			open := func(id uint32) *Session {
				t.Helper()
				if err := sender.writeOneSegment(testSessionSegment(openSessionRequest, id, common.PacketTransport), conn.LocalAddr()); err != nil {
					t.Fatal(err)
				}
				accepted := make(chan net.Conn, 1)
				go func() { c, _ := server.Accept(); accepted <- c }()
				select {
				case c := <-accepted:
					session, ok := c.(*Session)
					if !ok {
						t.Fatal("native session was not accepted")
					}
					waitForServerUserSessionBlock(t, session)
					return session
				case <-time.After(time.Second):
					t.Fatal("authenticated open was not accepted")
					return nil
				}
			}
			session, healthy := open(1), open(2)
			// Simulate elapsed idle time, retaining the actual socket and production maintenance interval.
			session.lastRXTime.Store(time.Now().Add(-idleSessionTimeout - time.Second).UnixMicro())
			if noise {
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() {
					defer close(done)
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ticker.C:
							_, _ = sender.conn.WriteTo([]byte{0}, conn.LocalAddr())
						case <-ctx.Done():
							return
						}
					}
				}()
				t.Cleanup(func() { cancel(); <-done })
			}
			deadline := time.Now().Add(sessionCleanInterval + 2*time.Second)
			for mux.ServerResourceStats().Sessions != 1 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if stats := mux.ServerResourceStats(); stats.Sessions != 1 || stats.BufferedBytes != 0 {
				t.Fatalf("expired peer retained resources without another valid packet: %+v", stats)
			}
			select {
			case <-healthy.Done():
				t.Fatal("idle cleanup closed a healthy session")
			default:
			}
			payload := []byte("still-alive")
			if err := sender.writeOneSegment(testDataSegment(2, 1, payload, common.PacketTransport), conn.LocalAddr()); err != nil {
				t.Fatal(err)
			}
			_ = healthy.SetReadDeadline(time.Now().Add(time.Second))
			got := make([]byte, len(payload))
			if n, err := healthy.Read(got); err != nil || n != len(payload) || !bytes.Equal(got, payload) {
				t.Fatalf("healthy traffic failed after idle cleanup: %q / %v", got[:n], err)
			}
		})
	}
}

func TestManagedExpiredSessionDoesNotWaitForItsStalledSendQueue(t *testing.T) {
	session, _ := managedPacketWire(t)
	pending := testDataSegment(session.id, 1, []byte("expired queued reply"), common.PacketTransport)
	pending.metadata.(*dataAckStruct).protocol = uint8(dataServerToClient)
	if !session.sendQueue.Insert(pending) {
		t.Fatal("fixture could not queue the expired reply")
	}
	session.nextSend.Store(2)
	session.remoteWindowSize.Store(0)
	session.lastRXTime.Store(time.Now().Add(-idleSessionTimeout - time.Second).UnixMicro())
	start := time.Now()
	session.conn.(*PacketUnderlay).cleanSessions()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("expired session delayed the shared packet loop for %s", elapsed)
	}
	select {
	case <-session.Done():
	default:
		t.Fatal("expired session did not terminate")
	}
	if session.sendQueue.Len() != 0 || session.conn.(*PacketUnderlay).SessionCount() != 0 {
		t.Fatal("expired session retained queued output or packet dispatch")
	}
}
