package dispatcher_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	. "github.com/xtls/xray-core/app/dispatcher"
	appstats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

type TestCounter int64

func (c *TestCounter) Value() int64 {
	return int64(*c)
}

func TestStatsWriterFreezeWaitsThroughDownstreamWrite(t *testing.T) {
	manager, err := appstats.NewManager(context.Background(), &appstats.Config{})
	if err != nil {
		t.Fatal(err)
	}
	counter, err := manager.GetOrRegisterCounter("user-writer")
	if err != nil {
		t.Fatal(err)
	}
	blocked := &pausedStatsWriter{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(blocked.release) }) }
	t.Cleanup(unblock)
	writer := &SizeStatWriter{Counter: counter, Writer: blocked}
	done := make(chan error, 1)
	go func() { done <- writer.WriteMultiBuffer(buf.MergeBytes(nil, []byte("abc"))) }()
	<-blocked.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = manager.SealCounters(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("freeze acknowledged active downstream write: %v", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, err := manager.SealCounters(context.Background())
	if err != nil || final["user-writer"] != 3 {
		t.Fatalf("writer freeze changed legacy byte count: %+v %v", final, err)
	}
	mb := buf.MergeBytes(nil, []byte("later"))
	if err := writer.WriteMultiBuffer(mb); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("frozen user writer accepted new payload: %v", err)
	}
	if mb[0] != nil {
		t.Fatal("frozen writer retained its owned buffer")
	}
}

type pausedStatsWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *pausedStatsWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	buf.ReleaseMulti(mb)
	close(w.entered)
	<-w.release
	return nil
}

func (c *TestCounter) Add(v int64) int64 {
	x := int64(*c) + v
	*c = TestCounter(x)
	return x
}

func (c *TestCounter) Set(v int64) int64 {
	*c = TestCounter(v)
	return v
}

func TestStatsWriter(t *testing.T) {
	var c TestCounter
	writer := &SizeStatWriter{
		Counter: &c,
		Writer:  buf.Discard,
	}

	mb := buf.MergeBytes(nil, []byte("abcd"))
	common.Must(writer.WriteMultiBuffer(mb))

	mb = buf.MergeBytes(nil, []byte("efg"))
	common.Must(writer.WriteMultiBuffer(mb))

	if c.Value() != 7 {
		t.Fatal("unexpected counter value. want 7, but got ", c.Value())
	}
}
