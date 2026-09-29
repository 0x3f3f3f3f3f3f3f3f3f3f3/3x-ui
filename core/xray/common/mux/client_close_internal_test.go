package mux

import (
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestConcurrentPickerCloseWaitsForIssuedCancellation(t *testing.T) {
	reader, writer := pipe.New()
	entered, release := make(chan struct{}), make(chan struct{})
	var unblockOnce, cancelOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(release) }) }
	worker, err := newClientWorker(transport.Link{Reader: reader, Writer: writer}, ClientStrategy{}, func() {
		cancelOnce.Do(func() { close(entered); <-release })
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unblock(); _ = worker.Close() })
	picker := &IncrementalWorkerPicker{workers: []*ClientWorker{worker}}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- picker.Close() }()
	<-entered
	go func() { second <- picker.Close() }()
	select {
	case err := <-second:
		t.Fatalf("concurrent Close acknowledged before worker cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}
