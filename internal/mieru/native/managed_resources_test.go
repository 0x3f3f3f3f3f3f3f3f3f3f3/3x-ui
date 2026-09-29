package protocol

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/enfein/mieru/v3/pkg/cipher"
	"github.com/enfein/mieru/v3/pkg/common"
	"github.com/enfein/mieru/v3/pkg/protocol/serveruser"
)

func TestManagedClosedUnderlayRejectsAnAllocatedSession(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		t.Run(network, func(t *testing.T) {
			mux := NewMux(false)
			t.Cleanup(func() { _ = mux.Close() })
			if err := mux.SetServerLimits(ServerLimits{Sessions: 2, SessionsPerUser: 1, QueueSegments: 64, QueueBytes: 65535, DisableUserMetrics: true}); err != nil {
				t.Fatal(err)
			}
			var base *baseUnderlay
			var underlay Underlay
			if network == "tcp" {
				stream := &StreamUnderlay{baseUnderlay: *mux.newServerUnderlay(1400, nil)}
				underlay, base = stream, &stream.baseUnderlay
			} else {
				packet := &PacketUnderlay{baseUnderlay: *mux.newServerUnderlay(1400, nil)}
				underlay, base = packet, &packet.baseUnderlay
			}
			policy := serveruser.BuildPolicies(rawUserMap("user", "password"))["user"]
			session := base.newServerSession(1, policy)
			if session == nil {
				t.Fatal("fixture failed to allocate a pending authenticated session")
			}
			if err := base.Close(); err != nil {
				t.Fatal(err)
			}
			err := underlay.AddSession(session, nil)
			if err == nil {
				// Avoid writing a shutdown frame on a fixture without an attached socket.
				session.forwardStateTo(sessionClosed)
				_ = session.closeWithError(io.ErrClosedPipe)
				session.wg.Wait()
			}
			base.discardServerSession(session)
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("closed native underlay accepted a session whose authentication was already in flight: %v", err)
			}
			if stats := mux.ServerResourceStats(); stats.Sessions != 0 || stats.BufferedBytes != 0 {
				t.Fatalf("rejected admission retained resources: %+v", stats)
			}
		})
	}
}

func TestManagedStaleCleanupPreservesReusedSessionTraffic(t *testing.T) {
	const username = "reused-session"
	credential := cipher.HashPassword([]byte(t.Name()), []byte(username))
	mux := NewMux(false).SetServerUsers(userMap(makeTestUser(username, credential)))
	t.Cleanup(func() { _ = mux.Close() })
	if err := mux.SetServerLimits(ServerLimits{Sessions: 2, SessionsPerUser: 2, QueueSegments: 64, QueueBytes: 65535, DisableUserMetrics: true}); err != nil {
		t.Fatal(err)
	}
	socket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &PacketUnderlay{baseUnderlay: *mux.newServerUnderlay(1400, nil), conn: socket, sessionCleanTicker: time.NewTicker(sessionCleanInterval), serverUsers: &mux.serverUsers}
	loopDone := make(chan error, 1)
	go func() { loopDone <- server.RunEventLoop(t.Context()) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = socket.Close()
		select {
		case <-loopDone:
		case <-time.After(time.Second):
			t.Error("native packet event loop did not stop")
		}
	})
	sender := newServerUserPacketSender(t, socket.LocalAddr(), credential, username)
	t.Cleanup(func() { _ = sender.conn.Close() })
	open := func() *Session {
		t.Helper()
		if err := sender.writeOneSegment(testSessionSegment(openSessionRequest, 7, common.PacketTransport), socket.LocalAddr()); err != nil {
			t.Fatal(err)
		}
		accepted := make(chan net.Conn, 1)
		go func() { conn, _ := server.Accept(); accepted <- conn }()
		select {
		case conn := <-accepted:
			if conn == nil {
				t.Fatal("native session was not accepted")
			}
			session := conn.(*Session)
			t.Cleanup(func() { _ = session.Close() })
			waitForServerUserSessionBlock(t, session)
			return session
		case <-time.After(time.Second):
			t.Fatal("native open request timed out")
			return nil
		}
	}
	previous := open()
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for mux.ServerResourceStats().Sessions != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := mux.ServerResourceStats(); stats.Sessions != 0 {
		t.Fatalf("original session did not finish before ID reuse: %+v", stats)
	}
	current := open()
	if err := server.RemoveSession(previous); err != nil {
		t.Fatal(err)
	}
	payload := []byte("replacement-session-payload")
	if err := sender.writeOneSegment(testDataSegment(7, 1, payload, common.PacketTransport), socket.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	_ = current.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, len(payload))
	if n, err := current.Read(got); err != nil || n != len(payload) || !bytes.Equal(got, payload) {
		t.Fatalf("stale cleanup removed the replacement session's packet dispatch: %q / %v", got[:n], err)
	}
}
