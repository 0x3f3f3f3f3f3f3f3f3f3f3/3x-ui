package xray

import (
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// TestProcessLifecycleFieldsRaceSafe drives the lifecycle fields (cmd, done,
// exitErr) the way Start/startCommand and the waitForCommand goroutine do, while
// the status getters read them concurrently. Run with -race: any unsynchronized
// access to those fields is reported as a data race.
func TestProcessLifecycleFieldsRaceSafe(t *testing.T) {
	p := &process{logWriter: NewLogWriter()}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: churn cmd/done/exitErr like Start + waitForCommand.
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			p.mu.Lock()
			p.cmd = &exec.Cmd{}
			p.done = make(chan struct{})
			p.mu.Unlock()
			p.setExitErr(errors.New("boom"))
		}
	})

	// Readers: the concurrent status getters.
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = p.IsRunning()
				_ = p.GetErr()
				_ = p.GetResult()
			}
		})
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestProcessVersionAPIPortRaceSafe writes version/apiPort the way Start's
// refresh helpers do while GetXrayVersion/GetAPIPort read them concurrently.
// Run with -race: an unsynchronized access to either field is reported.
func TestProcessVersionAPIPortRaceSafe(t *testing.T) {
	inner := &process{
		logWriter: NewLogWriter(),
		config:    &Config{InboundConfigs: []InboundConfig{{Tag: "api", Port: 12345}}},
	}
	p := &Process{inner}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			p.refreshAPIPort()
			inner.mu.Lock()
			inner.version = "v1.2.3"
			inner.mu.Unlock()
		}
	})

	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = p.GetAPIPort()
				_ = p.GetXrayVersion()
			}
		})
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestProcessStatusDuringActualCommandStart(t *testing.T) {
	path, err := exec.LookPath("true")
	if err != nil {
		t.Skip("requires the true command")
	}
	p := &process{logWriter: NewLogWriter()}
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = p.IsRunning()
			}
		}
	})
	defer func() { close(stop); readers.Wait() }()
	for range 30 {
		if err := p.startCommand(exec.Command(path)); err != nil {
			t.Fatal(err)
		}
		p.mu.RLock()
		done := p.done
		p.mu.RUnlock()
		<-done
		if p.IsRunning() {
			t.Fatal("finished command still reported running")
		}
	}
}
