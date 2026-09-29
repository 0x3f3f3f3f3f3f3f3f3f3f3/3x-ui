package buf

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	appstats "github.com/xtls/xray-core/app/stats"
)

func TestCounterFreezeWaitsForBufferedWrites(t *testing.T) {
	for _, vector := range []bool{false, true} {
		t.Run(map[bool]string{false: "write-all", true: "vector"}[vector], func(t *testing.T) {
			manager, err := appstats.NewManager(context.Background(), &appstats.Config{})
			if err != nil {
				t.Fatal(err)
			}
			counter, err := manager.GetOrRegisterCounter("buffered")
			if err != nil {
				t.Fatal(err)
			}
			writer := &pausedCounterWriter{completed: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			unblock := func() { release.Do(func() { close(writer.release) }) }
			t.Cleanup(unblock)
			done := make(chan error, 1)
			go func() {
				if vector {
					first, second := New(), New()
					_, _ = first.Write([]byte("abc"))
					_, _ = second.Write([]byte("def"))
					buffered := &BufferToBytesWriter{Writer: writer, counter: counter}
					done <- buffered.WriteMultiBuffer(MultiBuffer{first, second})
				} else {
					done <- WriteAllBytes(writer, []byte("abc"), counter)
				}
			}()
			<-writer.completed
			requirePendingCounterIO(t, manager)
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			final, err := manager.SealCounters(context.Background())
			want := int64(3)
			if vector {
				want = 6
			}
			if err != nil || final["buffered"] != want || int64(writer.Len()) != want {
				t.Fatalf("frozen buffered write lost completed bytes: %+v output=%q err=%v", final, writer.String(), err)
			}
		})
	}
}

func TestCounterFreezeCoversBufferedWriterByteEntrypoints(t *testing.T) {
	for _, path := range []string{"direct", "flush", "unbuffered"} {
		t.Run(path, func(t *testing.T) {
			manager, _ := appstats.NewManager(context.Background(), &appstats.Config{})
			counter, _ := manager.GetOrRegisterCounter("byte-writer")
			raw := &pausedCounterWriter{completed: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			unblock := func() { once.Do(func() { close(raw.release) }) }
			t.Cleanup(unblock)
			adapter := &BufferToBytesWriter{Writer: raw, counter: counter}
			buffered := NewBufferedWriter(adapter)
			if path == "unbuffered" {
				if err := buffered.SetBuffered(false); err != nil {
					t.Fatal(err)
				}
			}
			operation := func() error {
				if path == "direct" {
					_, err := adapter.Write([]byte("abc"))
					return err
				}
				if _, err := buffered.Write([]byte("abc")); err != nil {
					return err
				}
				return buffered.Flush()
			}
			done := make(chan error, 1)
			go func() { done <- operation() }()
			<-raw.completed
			requirePendingCounterIO(t, manager)
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			final, err := manager.SealCounters(context.Background())
			if err != nil || final["byte-writer"] != 3 || raw.String() != "abc" {
				t.Fatalf("byte write escaped accounting: %v output=%q err=%v", final, raw.String(), err)
			}
			if err := operation(); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("frozen byte writer reached raw IO: %v", err)
			}
			if raw.String() != "abc" {
				t.Fatal("sealed writer transmitted additional bytes")
			}
		})
	}
}

func TestCounterFreezeSettlesUnclaimedTimeoutReadOnce(t *testing.T) {
	manager, err := appstats.NewManager(context.Background(), &appstats.Config{})
	if err != nil {
		t.Fatal(err)
	}
	counter, err := manager.GetOrRegisterCounter("timeout-read")
	if err != nil {
		t.Fatal(err)
	}
	reader := &pausedCounterReader{started: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(reader.release) }) }
	t.Cleanup(unblock)
	timed := &TimeoutWrapperReader{Reader: reader, Counter: counter}
	if mb, err := timed.ReadMultiBufferTimeout(time.Millisecond); err != nil || len(mb) != 0 {
		ReleaseMulti(mb)
		t.Fatalf("pending read did not time out: %v", err)
	}
	<-reader.started
	requirePendingCounterIO(t, manager)
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	final, err := manager.SealCounters(ctx)
	if err != nil || final["timeout-read"] != 3 {
		t.Fatalf("unclaimed read escaped final accounting: %+v %v", final, err)
	}
	mb, err := timed.ReadMultiBuffer()
	defer ReleaseMulti(mb)
	if err != nil || mb.Len() != 3 || counter.Value() != 3 {
		t.Fatalf("retrieving completed read recounted or lost payload: bytes=%d total=%d err=%v", mb.Len(), counter.Value(), err)
	}
}

func requirePendingCounterIO(t *testing.T, manager *appstats.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.SealCounters(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("freeze acknowledged IO before its counter completed: %v", err)
	}
}

type pausedCounterWriter struct {
	bytes.Buffer
	completed chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (w *pausedCounterWriter) Write(b []byte) (int, error) {
	n, err := w.Buffer.Write(b)
	w.once.Do(func() { close(w.completed); <-w.release })
	return n, err
}

type pausedCounterReader struct {
	started chan struct{}
	release chan struct{}
	err     error
}

func (r *pausedCounterReader) ReadMultiBuffer() (MultiBuffer, error) {
	close(r.started)
	<-r.release
	return MergeBytes(nil, []byte("abc")), r.err
}

func TestCounterFreezeWaitsForCopyOptionIO(t *testing.T) {
	manager, err := appstats.NewManager(context.Background(), &appstats.Config{})
	if err != nil {
		t.Fatal(err)
	}
	counter, err := manager.GetOrRegisterCounter("copy-option")
	if err != nil {
		t.Fatal(err)
	}
	reader := &pausedCounterReader{started: make(chan struct{}), release: make(chan struct{}), err: io.EOF}
	var once sync.Once
	unblock := func() { once.Do(func() { close(reader.release) }) }
	t.Cleanup(unblock)
	done := make(chan error, 1)
	go func() { done <- Copy(reader, Discard, AddToStatCounter(counter)) }()
	<-reader.started
	requirePendingCounterIO(t, manager)
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, err := manager.SealCounters(context.Background())
	if err != nil || final["copy-option"] != 3 {
		t.Fatalf("copy option escaped frozen accounting: %+v %v", final, err)
	}
}
