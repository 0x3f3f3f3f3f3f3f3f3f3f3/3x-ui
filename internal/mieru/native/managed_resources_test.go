package protocol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/enfein/mieru/v3/pkg/cipher"
	"github.com/enfein/mieru/v3/pkg/common"
	"github.com/enfein/mieru/v3/pkg/protocol/serveruser"
)

func TestManagedPacketWindowRetainsEveryPromisedFragment(t *testing.T) {
	testManagedPacketWindow(t, false)
}

func TestManagedPacketWindowSurvivesInputSchedulingDelay(t *testing.T) {
	testManagedPacketWindow(t, true)
}

func TestManagedPacketOverloadRetainsEarlierRetransmission(t *testing.T) {
	for _, segments := range []int{64, 256} {
		t.Run(fmt.Sprintf("segments-%d", segments), func(t *testing.T) {
			mux := NewMux(false)
			t.Cleanup(func() { _ = mux.Close() })
			if err := mux.SetServerLimits(ServerLimits{Sessions: 1, SessionsPerUser: 1, QueueSegments: segments, QueueBytes: 128 << 10, DisableUserMetrics: true}); err != nil {
				t.Fatal(err)
			}
			base := mux.newServerUnderlay(1400, nil)
			session := base.newServerSession(1, serveruser.BuildPolicies(rawUserMap("ordered", "password"))["ordered"])
			if session == nil {
				t.Fatal("fixture failed to allocate a session")
			}
			t.Cleanup(func() { base.discardServerSession(session) })
			session.transportProtocol = common.PacketTransport
			capacity := min(segments, 128)
			fragment := func(seq int) *segment {
				return testDataSegment(1, uint32(seq), bytes.Repeat([]byte{byte(seq)}, 1024), common.PacketTransport)
			}
			for seq := range 2*capacity + 1 {
				if seq == capacity {
					continue
				}
				if err := session.queueManagedPacketSegment(fragment(seq)); err != nil {
					t.Fatal(err)
				}
			}
			if session.recvQueue.Len() != capacity || session.recvBuf.Len() != capacity {
				t.Fatal("fixture did not fill both bounded receive queues")
			}
			if err := session.queueManagedPacketSegment(fragment(capacity)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- session.runInputLoop(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			payload := make([]byte, 2*capacity*1024)
			for received := 0; received < len(payload); {
				_ = session.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
				n, err := session.Read(payload[received:])
				received += n
				if err != nil {
					t.Fatalf("earlier retransmission was discarded behind later fragments: read %d/%d bytes: %v", received, len(payload), err)
				}
			}
			for seq := range 2 * capacity {
				if !bytes.Equal(payload[seq*1024:(seq+1)*1024], fragment(seq).payload) {
					t.Fatalf("fragment %d was lost, duplicated or reordered", seq)
				}
			}
			if stats := mux.ServerResourceStats(); stats.BufferedBytes != 0 || stats.PeakBufferedBytes > int64(2*capacity*1024) {
				t.Fatalf("priority recovery exceeded or retained queue payload: %+v", stats)
			}
		})
	}
}

func testManagedPacketWindow(t *testing.T, staged bool) {
	t.Helper()
	mux := NewMux(false)
	t.Cleanup(func() { _ = mux.Close() })
	if err := mux.SetServerLimits(ServerLimits{Sessions: 1, SessionsPerUser: 1, QueueSegments: 256, QueueBytes: 128 << 10, DisableUserMetrics: true}); err != nil {
		t.Fatal(err)
	}
	base := mux.newServerUnderlay(1400, nil)
	session := base.newServerSession(1, serveruser.BuildPolicies(rawUserMap("window", "password"))["window"])
	if session == nil {
		t.Fatal("fixture failed to allocate the authenticated session")
	}
	t.Cleanup(func() { base.discardServerSession(session) })
	session.transportProtocol = common.PacketTransport
	window := session.receiveWindowSize()
	if window <= 0 {
		t.Fatal("an empty receiver must allow the sender to make progress")
	}
	payload := bytes.Repeat([]byte{42}, 1312)
	for seq := range window {
		fragment := testDataSegment(1, uint32(seq), payload, common.PacketTransport)
		if staged {
			if !base.deliverManagedSegment(session, fragment) {
				t.Fatal("active underlay refused the receiver's advertised packet")
			}
		} else if err := session.queueManagedPacketSegment(fragment); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- session.runInputLoop(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	received := 0
	buffer := make([]byte, 32<<10)
	for received < window*len(payload) {
		_ = session.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := session.Read(buffer)
		received += n
		if err != nil {
			t.Fatalf("receiver promised %d fragments but retained only %d/%d bytes: %v", window, received, window*len(payload), err)
		}
		if !bytes.Equal(buffer[:n], bytes.Repeat([]byte{42}, n)) {
			t.Fatal("advertised-window payload was corrupted")
		}
	}
}

func TestManagedPacketAckCoversOnlyRetainedContiguousData(t *testing.T) {
	for _, gap := range []bool{false, true} {
		t.Run(fmt.Sprintf("gap-%t", gap), func(t *testing.T) {
			session, readAck := managedPacketWire(t)
			deliver := func(seq int) {
				t.Helper()
				if err := session.queueManagedPacketSegment(testDataSegment(1, uint32(seq), make([]byte, 1024), common.PacketTransport)); err != nil {
					t.Fatal(err)
				}
			}
			for seq := range 257 {
				if (gap && seq == 128) || (!gap && seq == 256) {
					continue
				}
				deliver(seq)
			}
			deliver(0)
			deliver(257)
			assertAck := func(expected uint32) {
				t.Helper()
				session.ackDeadline.Store(0)
				session.runOutputOncePacket()
				ack := readAck()
				if ack.unAckSeq != expected || ack.windowSize != 0 {
					t.Fatalf("full receiver acknowledged next=%d window=%d, want retained contiguous next=%d window=0", ack.unAckSeq, ack.windowSize, expected)
				}
			}
			if gap {
				assertAck(128)
				deliver(128)
			}
			assertAck(256)
		})
	}
}

func managedPacketWire(t *testing.T) (*Session, func() *dataAckStruct) {
	t.Helper()
	mux := NewMux(false)
	t.Cleanup(func() { _ = mux.Close() })
	if err := mux.SetServerLimits(ServerLimits{Sessions: 1, SessionsPerUser: 1, QueueSegments: 256, QueueBytes: 128 << 10, DisableUserMetrics: true}); err != nil {
		t.Fatal(err)
	}
	wire, receiver, block := newTestPacketSession(t)
	base := mux.newServerUnderlay(1400, nil)
	session := base.newServerSession(1, serveruser.BuildPolicies(rawUserMap("window", "password"))["window"])
	if session == nil {
		t.Fatal("fixture failed to allocate the authenticated session")
	}
	t.Cleanup(func() { base.discardServerSession(session) })
	underlay := wire.conn.(*PacketUnderlay)
	underlay.isClient = false
	underlay.sessionMap.Store(session.id, session)
	session.block.Store(&underlay.block)
	session.conn, session.remoteAddr = underlay, receiver.LocalAddr()
	session.transportProtocol = common.PacketTransport
	session.forwardStateTo(sessionEstablished)
	return session, func() *dataAckStruct {
		t.Helper()
		_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
		packet := make([]byte, 65535)
		n, _, err := receiver.ReadFrom(packet)
		if err != nil {
			t.Fatal(err)
		}
		return decryptDataAckForTest(t, block, packet[:n])
	}
}

func TestManagedPacketOutputRefreshesQueuedAndRetransmittedWindow(t *testing.T) {
	session, readAck := managedPacketWire(t)
	reply := testDataSegment(1, 0, []byte("queued reply"), common.PacketTransport)
	reply.metadata.(*dataAckStruct).protocol = uint8(dataServerToClient)
	reply.metadata.(*dataAckStruct).windowSize = uint16(session.receiveWindowSize())
	if !session.sendQueue.Insert(reply) {
		t.Fatal("reply could not be queued")
	}
	for seq := range 99 {
		if !session.recvQueue.Insert(testDataSegment(1, uint32(seq), make([]byte, 1312), common.PacketTransport)) {
			t.Fatal("fixture failed to exhaust receive byte capacity")
		}
	}
	session.runOutputOncePacket()
	if window := readAck().windowSize; window != 0 {
		t.Fatalf("queued reply reopened a full receive window with %d stale credits", window)
	}
	session.recvQueue.DeleteAll()
	reply.txTime = time.Now().Add(-time.Second).UnixMicro()
	reply.txTimeout = time.Millisecond
	session.nextRetransmissionTime.Store(0)
	session.runOutputOncePacket()
	if window := readAck().windowSize; window == 0 {
		t.Fatal("retransmitted reply kept the empty receiver's stale zero window")
	}
}

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
