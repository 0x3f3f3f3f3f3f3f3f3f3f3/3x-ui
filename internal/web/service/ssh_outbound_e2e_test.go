//go:build linux

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"github.com/mhsanaei/3x-ui/v3/internal/sshoutbound"
	"github.com/mhsanaei/3x-ui/v3/internal/testutil/sshdtest"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func sshExitTarget(t *testing.T, prefix string) (string, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := new(atomic.Int64)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer conn.Close()
				for {
					var one [1]byte
					if _, err := io.ReadFull(conn, one[:]); err != nil {
						return
					}
					if _, err := conn.Write(append([]byte(prefix), one[:]...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), accepted
}

func sshExitDial(t *testing.T, address, target string) net.Conn {
	t.Helper()
	dialer, err := proxy.SOCKS5("tcp", address, nil, &net.Dialer{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func sshExitExchange(t *testing.T, conn net.Conn, prefix string) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(prefix)+1)
	if _, err := io.ReadFull(conn, response); err != nil || string(response) != prefix+"x" {
		t.Fatalf("actual selected egress response=%q want=%q err=%v", response, prefix+"x", err)
	}
}

func sshExitClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	n, err := conn.Read(one[:])
	var timeout net.Error
	if n != 0 || err == nil || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("exit did not close without payload: bytes=%d err=%v", n, err)
	}
}

func TestSSHOutboundRunsThroughProductionXray(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY for actual Xray-to-OpenSSH outbound")
	}
	upstream := sshdtest.Start(t)
	setupConflictDB(t)
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	t.Setenv("XUI_LOG_FOLDER", filepath.Join(binDir, "logs"))
	if err := os.Symlink(binary, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	bridgeAddress := productionSSHAddress(t)
	_, bridgePort, _ := net.SplitHostPort(bridgeAddress)
	t.Setenv("XUI_SSH_UPSTREAM_BRIDGE_PORT", bridgePort)
	target, sshCalls := sshExitTarget(t, "ssh:")
	directTarget, directCalls := sshExitTarget(t, "native:")
	addresses := map[string]string{}
	ports := map[string]int{}
	for _, tag := range []string{"api", "ssh-one", "ssh-two", "native-in"} {
		addresses[tag] = productionSSHAddress(t)
		_, p, _ := net.SplitHostPort(addresses[tag])
		ports[tag], _ = strconv.Atoi(p)
	}
	cfg := sshoutbound.Config{Address: upstream.Address, Port: upstream.Port, User: upstream.User, PrivateKey: upstream.PrivateKey, HostKey: upstream.HostKey}
	one := map[string]any{"tag": "upstream-one", "protocol": "ssh", "settings": cfg}
	two := map[string]any{"tag": "upstream-two", "protocol": "ssh", "settings": cfg}
	inbounds := []any{map[string]any{"tag": "api", "listen": "127.0.0.1", "port": ports["api"], "protocol": "tunnel", "settings": map[string]any{"address": "127.0.0.1"}}}
	for _, tag := range []string{"ssh-one", "ssh-two", "native-in"} {
		inbounds = append(inbounds, map[string]any{"tag": tag, "listen": "127.0.0.1", "port": ports[tag], "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}})
	}
	template := map[string]any{
		"log":       map[string]any{"loglevel": "warning"},
		"api":       map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"stats":     map[string]any{},
		"inbounds":  inbounds,
		"outbounds": []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, one, two, map[string]any{"tag": "native", "protocol": "freedom", "settings": map[string]any{"redirect": directTarget}}},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"},
			map[string]any{"type": "field", "domain": []string{"full:blocked.invalid"}, "outboundTag": "blocked"},
			map[string]any{"type": "field", "inboundTag": []string{"ssh-one"}, "outboundTag": "upstream-one"},
			map[string]any{"type": "field", "inboundTag": []string{"ssh-two"}, "outboundTag": "upstream-two"},
			map[string]any{"type": "field", "inboundTag": []string{"native-in"}, "outboundTag": "native"},
		}},
	}
	save := func() {
		t.Helper()
		encoded, err := json.Marshal(template)
		if err != nil {
			t.Fatal(err)
		}
		if err := (&XraySettingService{}).SaveXraySetting(string(encoded)); err != nil {
			t.Fatal(err)
		}
	}
	sshOutbounds := template["outbounds"]
	template["outbounds"] = []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "native", "protocol": "freedom", "settings": map[string]any{"redirect": directTarget}}}
	save()
	svc := &XrayService{}
	isManuallyStopped.Store(false)
	priorCrashHandler := xray.OnCrash
	crashSeen, releaseCrash := make(chan struct{}), make(chan struct{})
	var holdCrash atomic.Bool
	var seenOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCrash) }) }
	xray.OnCrash = func(error) {
		if holdCrash.Load() {
			seenOnce.Do(func() { close(crashSeen) })
			<-releaseCrash
		}
	}
	t.Cleanup(func() {
		_ = svc.StopXray()
		xray.OnCrash = priorCrashHandler
		isManuallyStopped.Store(false)
		isNeedXrayRestart.Store(false)
	})
	t.Cleanup(release)
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, addresses["native-in"])
	native := sshExitDial(t, addresses["native-in"], target)
	sshExitExchange(t, native, "native:")
	occupied, err := net.Listen("tcp", bridgeAddress)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = occupied.Close() })
	template["outbounds"] = sshOutbounds
	save()
	beforeConflict := currentXrayProcess()
	if err := svc.RestartXray(false); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("occupied bridge did not reject SSH application: %v", err)
	}
	if currentXrayProcess() != beforeConflict || xrayState.heldBackReason() == "" {
		t.Fatal("bridge collision replaced the working core or hid desired-state divergence")
	}
	sshExitExchange(t, native, "native:")
	_ = occupied.Close()
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, addresses["ssh-one"])
	active := sshExitDial(t, addresses["ssh-one"], target)
	stable := sshExitDial(t, addresses["ssh-two"], target)
	sshExitExchange(t, active, "ssh:")
	sshExitExchange(t, stable, "ssh:")
	sshExitExchange(t, native, "native:")
	if sshCalls.Load() != 2 || directCalls.Load() != 1 {
		t.Fatalf("independent exit counters: SSH=%d native=%d", sshCalls.Load(), directCalls.Load())
	}
	blocked := sshExitDial(t, addresses["ssh-one"], "blocked.invalid:80")
	_ = blocked.SetDeadline(time.Now().Add(time.Second))
	_, _ = blocked.Write([]byte("x"))
	sshExitClosed(t, blocked)
	if sshCalls.Load() != 2 || directCalls.Load() != 1 {
		t.Fatal("blocked-priority route reached an exit")
	}
	api := template["api"]
	delete(template, "api")
	save()
	if err := svc.RestartXray(false); err == nil || !strings.Contains(err.Error(), "core API") {
		t.Fatalf("missing readiness capability was not rejected: %v", err)
	}
	sshExitExchange(t, active, "ssh:")
	sshExitExchange(t, native, "native:")
	template["api"] = api
	bad := cfg
	bad.HostKey = sshOutboundTestConfig(t).HostKey
	one["settings"] = bad
	template["inbounds"] = append(inbounds, map[string]any{"tag": "invalid", "listen": "127.0.0.1", "port": 1, "protocol": "unknown-protocol"})
	save()
	prior := currentXrayProcess()
	if err := svc.RestartXray(false); err == nil || !strings.Contains(err.Error(), "validation") {
		t.Fatalf("invalid replacement core was not rejected by validation: %v", err)
	}
	if currentXrayProcess() != prior || !prior.IsRunning() || xrayState.heldBackReason() == "" {
		t.Fatal("invalid replacement lost the working core or its held-back status")
	}
	sshExitExchange(t, active, "ssh:")
	sshExitExchange(t, stable, "ssh:")
	sshExitExchange(t, native, "native:")
	beforeRotation := sshExitDial(t, addresses["ssh-one"], target)
	sshExitExchange(t, beforeRotation, "ssh:")
	_ = beforeRotation.Close()
	template["inbounds"] = inbounds
	save()
	if _, err := svc.GetXrayConfig(); err != nil {
		t.Fatal(err)
	}
	sshExitExchange(t, active, "ssh:")
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	sshExitClosed(t, active)
	sshExitExchange(t, stable, "ssh:")
	sshExitExchange(t, native, "native:")
	denied := sshExitDial(t, addresses["ssh-one"], target)
	_ = denied.SetDeadline(time.Now().Add(time.Second))
	_, _ = denied.Write([]byte("x"))
	sshExitClosed(t, denied)
	if sshCalls.Load() != 3 || directCalls.Load() != 1 {
		t.Fatal("incorrect host pin reached target through a fallback")
	}
	one["settings"] = cfg
	save()
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	restored := sshExitDial(t, addresses["ssh-one"], target)
	sshExitExchange(t, restored, "ssh:")
	sshExitExchange(t, stable, "ssh:")
	priorConfig := currentXrayProcess().GetConfig()
	one["settings"] = bad
	template["log"] = map[string]any{"loglevel": "error"}
	save()
	marker := filepath.Join(binDir, "fail-next-start")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(binDir, xray.GetBinaryName())
	if err := os.Remove(binaryPath); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	wrapper := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"-c\" ] && [ -f %s ]; then rm -- %s; exit 42; fi\nexec %s \"$@\"\n", quote(marker), quote(marker), quote(binary))
	if err := os.WriteFile(binaryPath, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(false); err == nil || !strings.Contains(err.Error(), "startup") {
		t.Fatalf("failed core startup did not reject replacement: %v", err)
	}
	if !currentXrayProcess().IsRunning() || !currentXrayProcess().GetConfig().Equals(priorConfig) || xrayState.heldBackReason() == "" {
		t.Fatal("failed startup did not restore applied core and report held-back state")
	}
	sshExitExchange(t, sshExitDial(t, addresses["ssh-one"], target), "ssh:")
	sshExitExchange(t, sshExitDial(t, addresses["native-in"], target), "native:")
	one["settings"] = cfg
	template["log"] = map[string]any{"loglevel": "warning"}
	save()
	if err := svc.RestartXray(false); err != nil || xrayState.heldBackReason() != "" {
		t.Fatalf("return to applied configuration did not clear held-back state: %v", err)
	}
	template["outbounds"] = []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "native", "protocol": "freedom", "settings": map[string]any{"redirect": directTarget}}}
	template["log"] = map[string]any{"loglevel": "error"}
	save()
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestartXray(false); err == nil || !strings.Contains(err.Error(), "startup") {
		t.Fatalf("failed core startup while removing upstreams committed deletion: %v", err)
	}
	if !currentXrayProcess().GetConfig().Equals(priorConfig) {
		t.Fatal("failed upstream removal lost the old applied configuration")
	}
	sshExitExchange(t, sshExitDial(t, addresses["ssh-one"], target), "ssh:")
	template["outbounds"] = sshOutbounds
	template["log"] = map[string]any{"loglevel": "warning"}
	save()
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	retired := sshExitDial(t, addresses["ssh-one"], target)
	sshExitExchange(t, retired, "ssh:")
	survivor := sshExitDial(t, addresses["native-in"], target)
	sshExitExchange(t, survivor, "native:")
	template["outbounds"] = []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "native", "protocol": "freedom", "settings": map[string]any{"redirect": directTarget}}}
	save()
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	sshExitClosed(t, retired)
	sshExitExchange(t, survivor, "native:")
	missing := sshExitDial(t, addresses["ssh-one"], target)
	_, _ = missing.Write([]byte("x"))
	sshExitClosed(t, missing)
	released, err := net.Listen("tcp", bridgeAddress)
	if err != nil {
		t.Fatalf("removing all upstreams retained the bridge port: %v", err)
	}
	_ = released.Close()
	template["outbounds"] = sshOutbounds
	save()
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	sshExitExchange(t, sshExitDial(t, addresses["ssh-one"], target), "ssh:")
	sshExitExchange(t, survivor, "native:")
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	sshExitClosed(t, restored)
	sshExitClosed(t, stable)
	probe, err := net.Listen("tcp", bridgeAddress)
	if err != nil {
		t.Fatalf("core stop retained upstream bridge: %v", err)
	}
	_ = probe.Close()
	if err := svc.RestartXray(true); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, addresses["ssh-one"])
	restarted := sshExitDial(t, addresses["ssh-one"], target)
	sshExitExchange(t, restarted, "ssh:")
	holdCrash.Store(true)
	killSSHExitCore(t, binary, filepath.Join(binDir, "config.json"))
	select {
	case <-crashSeen:
	case <-time.After(time.Second):
		t.Fatal("killed core did not reach its crash lifecycle callback")
	}
	unfinished := currentXrayProcess()
	if err := svc.RestartXray(true); err == nil || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("unfinished core stop was not refused: %v", err)
	}
	if currentXrayProcess() != unfinished || !strings.Contains(xrayState.heldBackReason(), "stop") {
		t.Fatal("core replacement ignored an unconfirmed stop")
	}
	release()
	sshExitClosed(t, restarted)
	deadline := time.Now().Add(time.Second)
	for {
		probe, err := net.Listen("tcp", bridgeAddress)
		if err == nil {
			_ = probe.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("crashed core retained upstream listener: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := svc.RestartXray(false); err != nil {
		t.Fatal(err)
	}
	productionSSHWait(t, addresses["ssh-one"])
	sshExitExchange(t, sshExitDial(t, addresses["ssh-one"], target), "ssh:")
	beforeLostUpstream := sshCalls.Load()
	upstream.Stop()
	unavailable := sshExitDial(t, addresses["ssh-one"], target)
	_, _ = unavailable.Write([]byte("x"))
	sshExitClosed(t, unavailable)
	if sshCalls.Load() != beforeLostUpstream {
		t.Fatal("dead upstream fell back to a direct target connection")
	}
	sshExitExchange(t, sshExitDial(t, addresses["native-in"], target), "native:")
	if err := svc.StopXray(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetXrayConfig(); err != nil {
		t.Fatal(err)
	}
	delete(template, "api")
	template["outbounds"] = []any{map[string]any{"tag": "blocked", "protocol": "blackhole"}, map[string]any{"tag": "native", "protocol": "freedom", "settings": map[string]any{"redirect": directTarget}}}
	save()
	if err := svc.RestartXray(true); err != nil {
		t.Fatalf("unused SSH preview imposed API capability on native-only core: %v", err)
	}
	productionSSHWait(t, addresses["native-in"])
	sshExitExchange(t, sshExitDial(t, addresses["native-in"], target), "native:")
	probe, err = net.Listen("tcp", bridgeAddress)
	if err != nil {
		t.Fatalf("unused SSH preview started an upstream listener: %v", err)
	}
	_ = probe.Close()
}

func killSSHExitCore(t *testing.T, binaryPath, configPath string) {
	t.Helper()
	paths, err := filepath.Glob("/proc/[0-9]*/cmdline")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		args := strings.Split(string(data), "\x00")
		if err != nil || len(args) < 3 || args[0] != binaryPath || args[1] != "-c" || args[2] != configPath {
			continue
		}
		pid, err := strconv.Atoi(filepath.Base(filepath.Dir(path)))
		if err != nil {
			t.Fatal(err)
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Kill(); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("test-owned Xray process not found")
}
