package policyflow

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
)

type udpReplyWriter struct {
	conn *net.UDPConn
	peer *net.UDPAddr
}

func (w udpReplyWriter) Write(p []byte) (int, error) { return w.conn.WriteToUDP(p, w.peer) }

func udpFlowPair(t *testing.T) (*net.UDPConn, *net.UDPConn) {
	t.Helper()
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	client, err := net.DialUDP("udp4", nil, target.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return target, client
}

func receiveDatagram(t *testing.T, conn *net.UDPConn, want []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var p [65535]byte
	n, _, err := conn.ReadFromUDP(p[:])
	if err != nil || !bytes.Equal(p[:n], want) {
		t.Fatalf("actual UDP datagram = %q / %v; want %q", p[:n], err, want)
	}
}

func TestDatagramQuotaRejectsWholePacketAndKeepsSmallerAllowance(t *testing.T) {
	db := flowDB(t)
	client := flowClient(t, db, 15)
	other := flowClient(t, db, 1000)
	ledger := database.NewClientUsageLedger(db)
	if _, err := ledger.ChangeMultiplier(t.Context(), client.PolicyID, 1, 1500, nil); err != nil {
		t.Fatal(err)
	}
	controller := flowController(t, ledger)
	for _, id := range []string{client.PolicyID, other.PolicyID} {
		if err := controller.Configure(t.Context(), id, Rates{}); err != nil {
			t.Fatal(err)
		}
	}
	target, peer := udpFlowPair(t)
	flow, err := controller.Open(t.Context(), client.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	up := flow.DatagramWriter(Upload, peer)
	down := flow.DatagramWriter(Download, udpReplyWriter{conn: target, peer: peer.LocalAddr().(*net.UDPAddr)})
	if n, err := up.Write([]byte("123456")); n != 6 || err != nil {
		t.Fatalf("upload = %d, %v", n, err)
	}
	receiveDatagram(t, target, []byte("123456"))
	if n, err := down.Write([]byte("abcdef")); n != 0 || !errors.Is(err, database.ErrUsageQuota) {
		t.Fatalf("oversized quota packet = %d, %v; must reject without a partial write", n, err)
	}
	account, err := ledger.Read(t.Context(), client.PolicyID)
	if err != nil || account.Up != 6 || account.Down != 0 || account.Billed != 9 {
		t.Fatalf("rejected datagram changed accounting: %+v / %v", account, err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	var p [16]byte
	if n, err := peer.Read(p[:]); n != 0 || err == nil {
		t.Fatalf("a rejected packet leaked bytes to the actual UDP peer: %d / %v", n, err)
	} else {
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("UDP peer failed for a reason other than no delivered packet: %v", err)
		}
	}
	if n, err := down.Write([]byte("abcd")); n != 4 || err != nil {
		t.Fatalf("valid smaller packet lost remaining quota: %d, %v", n, err)
	}
	receiveDatagram(t, peer, []byte("abcd"))
	account, err = ledger.Read(t.Context(), client.PolicyID)
	if err != nil || account.Up != 6 || account.Down != 4 || account.Billed != 15 {
		t.Fatalf("duplex datagram billing = %+v / %v", account, err)
	}
	if n, err := up.Write([]byte("x")); n != 0 || !errors.Is(err, database.ErrUsageQuota) {
		t.Fatalf("exhausted UDP flow still sends: %d, %v", n, err)
	}
	otherFlow, err := controller.Open(t.Context(), other.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	defer otherFlow.Close()
	if n, err := otherFlow.DatagramWriter(Upload, peer).Write([]byte("independent")); n != 11 || err != nil {
		t.Fatalf("same-IP client was affected by another quota: %d, %v", n, err)
	}
	receiveDatagram(t, target, []byte("independent"))
}

func TestEmptyDatagramPreservesBoundaryWithoutBypassingDisable(t *testing.T) {
	db := flowDB(t)
	client := flowClient(t, db, 1000)
	ledger := database.NewClientUsageLedger(db)
	controller := flowController(t, ledger)
	if err := controller.Configure(t.Context(), client.PolicyID, Rates{}); err != nil {
		t.Fatal(err)
	}
	flow, err := controller.Open(t.Context(), client.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	target, peer := udpFlowPair(t)
	writer := flow.DatagramWriter(Upload, peer)
	if n, err := writer.Write(nil); n != 0 || err != nil {
		t.Fatalf("empty datagram = %d / %v", n, err)
	}
	receiveDatagram(t, target, nil)
	account, err := ledger.Read(t.Context(), client.PolicyID)
	if err != nil || account.Up != 0 || account.Down != 0 || account.Billed != 0 {
		t.Fatalf("empty datagram invented payload bytes: %+v / %v", account, err)
	}
	if err := db.Model(&client).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := writer.Write(nil); n != 0 || !errors.Is(err, database.ErrUsageDisabled) {
		t.Fatalf("empty datagram bypassed manual disable: %d / %v", n, err)
	}
	select {
	case <-flow.Context().Done():
		if !errors.Is(context.Cause(flow.Context()), database.ErrUsageDisabled) {
			t.Fatalf("wrong flow revocation cause: %v", context.Cause(flow.Context()))
		}
	case <-time.After(time.Second):
		t.Fatal("disabled UDP flow was not retired")
	}
}
