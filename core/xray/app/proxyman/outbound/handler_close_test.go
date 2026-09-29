package outbound

import (
	"context"
	"errors"
	"testing"

	xerrors "github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/mux"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestOutboundCloseIncludesBothMuxPoolsAndProxyFailure(t *testing.T) {
	makePool := func() (*mux.ClientManager, *mux.ClientWorker) {
		reader, writer := pipe.New()
		worker, err := mux.NewClientWorker(transport.Link{Reader: reader, Writer: writer}, mux.ClientStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = worker.Close() })
		picker := &mux.IncrementalWorkerPicker{Factory: &closeWorkerFactory{worker: worker}}
		if _, err := picker.PickAvailable(); err != nil {
			t.Fatal(err)
		}
		return &mux.ClientManager{Enabled: true, Picker: picker}, worker
	}
	tcpPool, tcpWorker := makePool()
	udpPool, udpWorker := makePool()
	wantErr := errors.New("outbound close failure")
	proxy := &closeFailureProxy{err: wantErr}
	handler := &Handler{mux: tcpPool, xudp: udpPool, proxy: proxy}
	err := handler.Close()
	if !tcpWorker.Closed() || !udpWorker.Closed() {
		t.Fatalf("outbound close leaked pools: tcp=%v udp=%v", tcpWorker.Closed(), udpWorker.Closed())
	}
	if !proxy.closed || !xerrors.AllEqual(wantErr, err) {
		t.Fatalf("outbound lost proxy close failure: closed=%v err=%v", proxy.closed, err)
	}
	if err := (&Handler{}).Close(); err != nil {
		t.Fatalf("empty outbound close: %v", err)
	}
}

type closeWorkerFactory struct{ worker *mux.ClientWorker }

func (f *closeWorkerFactory) Create() (*mux.ClientWorker, error) { return f.worker, nil }

type closeFailureProxy struct {
	closed bool
	err    error
}

func (*closeFailureProxy) Process(context.Context, *transport.Link, internet.Dialer) error {
	return nil
}

func (p *closeFailureProxy) Close() error { p.closed = true; return p.err }
