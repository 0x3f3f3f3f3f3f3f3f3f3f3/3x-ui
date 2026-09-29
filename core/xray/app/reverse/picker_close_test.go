package reverse

import (
	"testing"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/mux"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

func TestStaticPickerCloseClosesExistingAndLateWorkers(t *testing.T) {
	makeWorker := func() *PortalWorker {
		reader, _ := pipe.New()
		_, writer := pipe.New()
		client, err := mux.NewClientWorker(transport.Link{Reader: reader, Writer: writer}, mux.ClientStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		worker, err := NewPortalWorker(client)
		if err != nil {
			t.Fatal(err)
		}
		return worker
	}
	picker, err := NewStaticMuxPicker()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = common.Close(picker) })
	first := makeWorker()
	picker.AddWorker(first)
	if err := common.Close(picker); err != nil {
		t.Fatal(err)
	}
	if !first.Closed() {
		t.Fatal("static mux picker retained an active worker")
	}
	late := makeWorker()
	picker.AddWorker(late)
	if !late.Closed() {
		t.Fatal("closed static mux picker admitted a late worker")
	}
	if worker, err := picker.PickAvailable(); worker != nil || err == nil {
		t.Fatalf("closed static mux picker selected a worker: %v %v", worker, err)
	}
}
