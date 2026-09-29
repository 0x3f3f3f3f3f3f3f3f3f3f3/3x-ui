package mieru

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	apimodel "github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/cipher"
	"github.com/enfein/mieru/v3/pkg/protocol"
	"github.com/enfein/mieru/v3/pkg/stderror"

	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestNativeTCPMultiplexingBackpressureIsConnectionScoped(t *testing.T) {
	db, ledger, controller := mieruDB(t)
	record, user := mieruUser(t, db, ledger, controller, 1000)
	if err := db.Model(&record).Update("total_gb", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", record.Email).Update("total", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := controller.Configure(t.Context(), user.PolicyID, policyflow.Rates{Upload: 16384}); err != nil {
		t.Fatal(err)
	}
	server := startNative(t, controller, "tcp", user)
	target := nativeEcho(t, "tcp")
	newUnderlay := func() *protocol.StreamUnderlay {
		block, err := cipher.BlockCipherFromPassword(cipher.HashPassword([]byte(user.Password), []byte(user.Username)), false)
		if err != nil {
			t.Fatal(err)
		}
		block.SetBlockContext(cipher.BlockContext{UserName: user.Username})
		u, err := protocol.NewStreamUnderlay(t.Context(), &net.Dialer{}, net.DefaultResolver, nil, "tcp", server.Addresses()[0].String(), 1400, block, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- u.RunEventLoop(ctx) }()
		t.Cleanup(func() { cancel(); _ = u.Close(); <-done })
		return u
	}
	open := func(u *protocol.StreamUnderlay, id uint32) *protocol.Session {
		s := protocol.NewSession(id, true, 1400, nil, nil)
		if err := u.AddSession(s, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Write(nativeRequest(t, target)); err != nil {
			t.Fatal(err)
		}
		return s
	}
	readReply := func(s *protocol.Session, d time.Duration) error {
		_, err := apimodel.ReadSocks5Response(handshakeReader{conn: s, deadline: time.Now().Add(d)})
		return err
	}
	shared := newUnderlay()
	first := open(shared, 1)
	if err := readReply(first, time.Second); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, first); close(readDone) }()
	t.Cleanup(func() { _ = shared.Close(); <-readDone })
	writeDone := make(chan error, 1)
	go func() { _, err := first.Write(bytes.Repeat([]byte("x"), 1<<20)); writeDone <- err }()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fixture did not buffer its finite TCP payload")
	}
	deadline := time.Now().Add(time.Second)
	for server.mux.ServerResourceStats().BufferedBytes < 128<<10 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if server.mux.ServerResourceStats().BufferedBytes < 128<<10 {
		t.Fatal("fixture did not fill managed receive buffers")
	}
	second := open(shared, 2)
	if err := readReply(second, 250*time.Millisecond); err == nil {
		t.Fatal("shared TCP handshake bypassed the queued payload used to reproduce head-of-line blocking")
	} else if !errors.Is(err, stderror.ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared TCP handshake failed instead of waiting: %v", err)
	}
	separate := open(newUnderlay(), 3)
	if err := readReply(separate, time.Second); err != nil {
		t.Fatalf("independent underlay also blocked: %v", err)
	}
	if err := controller.Configure(t.Context(), user.PolicyID, policyflow.Rates{}); err != nil {
		t.Fatal(err)
	}
	if err := readReply(second, 2*time.Second); err != nil {
		t.Fatalf("unblocking payload did not release multiplexed handshake: %v", err)
	}
	if stats := server.mux.ServerResourceStats(); stats.PeakBufferedBytes > 5*(128<<10) {
		t.Fatalf("handshake recovery exceeded the fixed session buffer bound: %+v", stats)
	}
}
