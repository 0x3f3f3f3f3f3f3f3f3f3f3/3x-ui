package mux_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/mux"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
)

func TestClientManagerCloseCancelsIdleProxyAndSealsPicker(t *testing.T) {
	proxy := &waitingMuxProxy{started: make(chan struct{}), stopped: make(chan struct{}), abort: make(chan struct{})}
	t.Cleanup(func() { close(proxy.abort) })
	factory := &mux.DialingWorkerFactory{Proxy: proxy, Strategy: mux.ClientStrategy{MaxConcurrency: 8, MaxConnection: 128}}
	picker := &mux.IncrementalWorkerPicker{Factory: factory}
	manager := &mux.ClientManager{Enabled: true, Picker: picker}
	worker, err := picker.PickAvailable()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close() })
	<-proxy.started
	if err := common.Close(manager); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxy.stopped:
	case <-time.After(time.Second):
		t.Fatal("closing mux manager left its idle proxy context alive")
	}
	if !worker.Closed() {
		t.Fatal("closing mux manager left its worker open")
	}
	if _, err := picker.PickAvailable(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed picker admitted a new worker: %v", err)
	}
	if err := common.Close(manager); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
}

type waitingMuxProxy struct {
	started chan struct{}
	stopped chan struct{}
	abort   chan struct{}
}

func (p *waitingMuxProxy) Process(ctx context.Context, _ *transport.Link, _ internet.Dialer) error {
	close(p.started)
	defer close(p.stopped)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.abort:
		return nil
	}
}

func TestClientManagerCloseDoesNotWaitForFactoryOrPublishLateWorker(t *testing.T) {
	proxy := &waitingMuxProxy{started: make(chan struct{}), stopped: make(chan struct{}), abort: make(chan struct{})}
	t.Cleanup(func() { close(proxy.abort) })
	factory := &blockedWorkerFactory{
		factory: &mux.DialingWorkerFactory{Proxy: proxy, Strategy: mux.ClientStrategy{MaxConcurrency: 8, MaxConnection: 128}},
		entered: make(chan struct{}), release: make(chan struct{}), created: make(chan *mux.ClientWorker, 1),
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(factory.release) }) }
	t.Cleanup(unblock)
	picker := &mux.IncrementalWorkerPicker{Factory: factory}
	manager := &mux.ClientManager{Enabled: true, Picker: picker}
	picked := make(chan error, 1)
	go func() { _, err := picker.PickAvailable(); picked <- err }()
	<-factory.entered
	closed := make(chan error, 1)
	go func() { closed <- common.Close(manager) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("manager close waited behind a blocked worker factory")
	}
	rejected := make(chan error, 1)
	go func() { _, err := picker.PickAvailable(); rejected <- err }()
	select {
	case err := <-rejected:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("closed picker accepted new admission: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("new admission after close waited behind a blocked worker factory")
	}
	unblock()
	worker := <-factory.created
	t.Cleanup(func() { _ = worker.Close() })
	if err := <-picked; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed picker published a late worker: %v", err)
	}
	if !worker.Closed() {
		t.Fatal("late worker was leaked")
	}
	select {
	case <-proxy.stopped:
	case <-time.After(time.Second):
		t.Fatal("late worker's proxy context was leaked")
	}
}

type blockedWorkerFactory struct {
	factory mux.ClientWorkerFactory
	entered chan struct{}
	release chan struct{}
	created chan *mux.ClientWorker
}

func (f *blockedWorkerFactory) Create() (*mux.ClientWorker, error) {
	close(f.entered)
	<-f.release
	worker, err := f.factory.Create()
	f.created <- worker
	return worker, err
}
