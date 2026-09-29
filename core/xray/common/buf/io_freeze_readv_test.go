//go:build !wasm && !openbsd

package buf

import (
	"context"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	appstats "github.com/xtls/xray-core/app/stats"
)

func TestCounterFreezeWaitsForReadvCompletion(t *testing.T) {
	manager, err := appstats.NewManager(context.Background(), &appstats.Config{})
	if err != nil {
		t.Fatal(err)
	}
	counter, err := manager.GetOrRegisterCounter("readv")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	_ = server.SetDeadline(time.Now().Add(time.Second))
	raw, err := server.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	paused := &pausedCounterRawConn{RawConn: raw, completed: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(paused.release) }) }
	t.Cleanup(unblock)
	reader := NewReadVReader(server, paused, counter)
	reader.alloc.current = 2
	done := make(chan error, 1)
	go func() {
		mb, err := reader.ReadMultiBuffer()
		ReleaseMulti(mb)
		done <- err
	}()
	if _, err := client.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	<-paused.completed
	requirePendingCounterIO(t, manager)
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, err := manager.SealCounters(context.Background())
	if err != nil || final["readv"] != 3 {
		t.Fatalf("readv completed outside accounting boundary: %+v %v", final, err)
	}
}

type pausedCounterRawConn struct {
	syscall.RawConn
	completed chan struct{}
	release   chan struct{}
}

func (c *pausedCounterRawConn) Read(fn func(uintptr) bool) error {
	err := c.RawConn.Read(fn)
	close(c.completed)
	<-c.release
	return err
}
