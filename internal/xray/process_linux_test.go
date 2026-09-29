//go:build linux

package xray

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestXrayChildSurvivesStartingThreadExit(t *testing.T) {
	initProcessTestLogger(t)
	readyPath := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestXrayLifetimeEchoHelper$")
	cmd.Env = append(os.Environ(), "XRAY_LIFETIME_ECHO="+readyPath)
	p := newProcess(nil)
	t.Cleanup(func() {
		if p.IsRunning() {
			p.intentionalStop.Store(true)
			_ = cmd.Process.Kill()
			if err := p.waitForExit(2 * time.Second); err != nil {
				t.Error(err)
			}
		}
	})
	type result struct {
		tid int
		err error
	}
	started := make(chan result, 1)
	releaseMain := make(chan struct{})
	defer close(releaseMain)
	releaseCaller := make(chan struct{}, 1)
	defer close(releaseCaller)
	start := func() {
		started <- result{syscall.Gettid(), p.startCommand(cmd)}
		<-releaseCaller
	}
	go func() {
		// Exiting while locked forces this caller's OS thread to terminate.
		runtime.LockOSThread()
		if syscall.Gettid() == os.Getpid() {
			// Go parks its initial thread instead of terminating it on Linux.
			go func() { runtime.LockOSThread(); start() }()
			<-releaseMain
			runtime.UnlockOSThread()
			return
		}
		start()
	}()
	res := <-started
	if res.err != nil {
		t.Fatal(res.err)
	}
	waitForProcessHelperReady(t, readyPath)
	releaseCaller <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := os.Stat("/proc/self/task/" + strconv.Itoa(res.tid))
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("starting OS thread did not exit: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	address, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", string(address), time.Second)
	if err != nil {
		t.Fatalf("child stopped with its caller's thread: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte("alive")); err != nil {
		t.Fatal(err)
	}
	var reply [5]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || string(reply[:]) != "alive" {
		t.Fatalf("child did not serve after caller thread exited: %q, %v", reply, err)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestXrayLifetimeEchoHelper(t *testing.T) {
	path := os.Getenv("XRAY_LIFETIME_ECHO")
	if path == "" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.WriteFile(path, []byte(listener.Addr().String()), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
	}
}
