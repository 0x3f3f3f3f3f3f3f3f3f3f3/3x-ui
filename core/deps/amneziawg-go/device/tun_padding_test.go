package device

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn/bindtest"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/tuntest"
)

type observedReadTUN struct {
	tun.Device
	reads chan int
}

func (t *observedReadTUN) Read(packets [][]byte, sizes []int, offset int) (int, error) {
	t.reads <- offset
	return t.Device.Read(packets, sizes, offset)
}

func TestDeviceTransportPaddingChangesDuringPendingTUNRead(t *testing.T) {
	goroutineLeakCheck(t)
	var pair testPair
	var observed [2]*observedReadTUN
	binds := bindtest.NewChannelBinds()
	cfg, endpoints := genConfigs(t)
	for i := range pair {
		p := &pair[i]
		p.tun = tuntest.NewChannelTUN()
		p.ip = netip.AddrFrom4([4]byte{1, 0, 0, byte(i + 1)})
		observed[i] = &observedReadTUN{Device: p.tun.TUN(), reads: make(chan int, 1)}
		p.dev = NewDevice(observed[i], binds[i], NewLogger(LogLevelError, ""))
		t.Cleanup(p.dev.Close)
		if err := p.dev.IpcSet(cfg[i]); err != nil {
			t.Fatal(err)
		}
		if err := p.dev.Up(); err != nil {
			t.Fatal(err)
		}
		endpoints[i^1] = fmt.Sprintf(endpoints[i^1], p.dev.net.port)
	}
	for i := range pair {
		if err := pair[i].dev.IpcSet(endpoints[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, padding := range []int{25, 7, 0} {
		for i := range observed {
			select {
			case <-observed[i].reads:
			case <-time.After(5 * time.Second):
				t.Fatalf("device %d did not enter its next TUN read", i)
			}
		}
		for i := range pair {
			if err := pair[i].dev.IpcSet(uapiCfg("s4", fmt.Sprint(padding))); err != nil {
				t.Fatal(err)
			}
		}
		pair.Send(t, Ping, nil)
		pair.Send(t, Pong, nil)
		if t.Failed() {
			t.Fatalf("first packets after changing S4 to %d did not transit intact", padding)
		}
	}
}
