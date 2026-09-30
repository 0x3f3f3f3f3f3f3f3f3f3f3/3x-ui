package amneziawgnet

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/device"
)

func TestAmneziaTimerRearmWhileExpiringHasNoDataRace(t *testing.T) {
	var expirations, observations atomic.Int64
	var invalidDuration atomic.Bool
	timer := (&device.Peer{}).NewTimer(func(_ *device.Peer, duration time.Duration) {
		if duration != time.Nanosecond {
			invalidDuration.Store(true)
		}
		expirations.Add(1)
	})
	t.Cleanup(timer.DelSync)
	stop, observed := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(observed)
		for {
			select {
			case <-stop:
				return
			default:
				timer.IsPending()
				observations.Add(1)
				runtime.Gosched()
			}
		}
	}()
	for range 4000 {
		timer.Mod(time.Nanosecond)
		runtime.Gosched()
	}
	close(stop)
	<-observed
	timer.DelSync()
	if expirations.Load() == 0 || observations.Load() == 0 || invalidDuration.Load() {
		t.Fatalf("timer did not expire with the configured interval during concurrent observation: expirations=%d observations=%d invalid=%v", expirations.Load(), observations.Load(), invalidDuration.Load())
	}
	if timer.IsPending() {
		t.Fatal("synchronously deleted timer remained pending")
	}
	t.Logf("expired %d times with %d concurrent state observations", expirations.Load(), observations.Load())
}
