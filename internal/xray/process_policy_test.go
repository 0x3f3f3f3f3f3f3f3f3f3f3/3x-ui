package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/clientpolicy"
	policycommand "github.com/xtls/xray-core/app/clientpolicy/command"
)

func TestManagedProcessNegotiatesAndSeedsBeforeOpeningListeners(t *testing.T) {
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the built custom core")
	}
	for _, mode := range []string{"success", "quota-window", "slow-preparation", "preparation-failure", "listener-conflict"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "panel-managed-start-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			t.Setenv("XUI_BIN_FOLDER", dir)
			t.Setenv("XUI_LOG_FOLDER", dir)
			if err := os.Symlink(binary, filepath.Join(dir, GetBinaryName())); err != nil {
				t.Fatal(err)
			}
			state, socket := filepath.Join(dir, "state.db"), filepath.Join(dir, "control.sock")
			if err := clientpolicy.CreateStore(state, "managed-process"); err != nil {
				t.Fatal(err)
			}
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			go func() {
				conn, err := target.Accept()
				if err == nil {
					defer conn.Close()
					_, _ = io.Copy(conn, conn)
				}
			}()
			port := freePort(t)
			address := fmt.Sprintf("127.0.0.1:%d", port)
			raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService"]},"clientPolicy":{"stateFile":%q,"instanceId":"managed-process","policies":[{"clientId":"owner","version":1,"enabled":true,"multiplierMicros":1000000,"quotaBytes":1000,"burstBytes":65536}]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":"owner"}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, socket, state, port, target.Addr().(*net.TCPAddr).Port)
			if mode == "quota-window" {
				raw = strings.Replace(raw, `"quotaBytes":1000`, `"quotaBytes":600,"quotaBaselineBytes":100,"quotaBaselineRemainder":500000`, 1)
			}
			var config Config
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			if mode == "listener-conflict" {
				conflict := config.InboundConfigs[0]
				conflict.Tag, conflict.Port = "conflict", target.Addr().(*net.TCPAddr).Port
				config.InboundConfigs = append(config.InboundConfigs, conflict)
			}
			process := NewTestProcess(&config, filepath.Join(dir, "bootstrap.json"))
			t.Cleanup(func() { _ = process.Stop() })
			if err := process.Start(); !errors.Is(err, ErrClientPolicyCapability) || process.IsRunning() {
				t.Fatalf("plain startup bypassed negotiation: running=%t, error=%v", process.IsRunning(), err)
			}
			activationTimeout := 10 * time.Second
			if mode == "slow-preparation" {
				activationTimeout = 20 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), activationTimeout)
			defer cancel()
			errSetup := errors.New("panel ledger transaction failed")
			err = process.StartManaged(ctx, func(ctx context.Context, api *ClientPolicyAPI) error {
				proof := process.NativeTrafficConfigProof()
				if proof == nil || proof.ConfigStable {
					t.Error("control-only bootstrap acquired legacy configuration eligibility")
				}
				if process.IsControlReady() {
					t.Error("runtime control admitted operations before usage preparation")
				}
				if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
					conn.Close()
					t.Error("business listener opened before capability negotiation and seeding")
				}
				if mode == "preparation-failure" {
					return errSetup
				}
				if mode == "slow-preparation" {
					select {
					case <-time.After(11 * time.Second):
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				seedPolicy := &clientpolicy.PolicyConfig{ClientId: "owner", Version: 1, Enabled: true, MultiplierMicros: 1000000, QuotaBytes: 1000, BurstBytes: 65536}
				seedUsage := &policycommand.Usage{RawDownload: 100, BilledBytes: 100}
				if mode == "quota-window" {
					seedPolicy.QuotaBytes, seedPolicy.QuotaBaselineBytes, seedPolicy.QuotaBaselineRemainder = 600, 100, 500000
					seedUsage.Remainder = 500000
				}
				return api.Initialize(ctx, seedPolicy, seedUsage)
			})
			if mode != "success" && mode != "quota-window" && mode != "slow-preparation" {
				if mode == "preparation-failure" && !errors.Is(err, errSetup) {
					t.Fatalf("lost preparation failure: %v", err)
				}
				if mode == "listener-conflict" && (err == nil || !strings.Contains(err.Error(), "managed listener activation")) {
					t.Fatalf("lost listener conflict: %v", err)
				}
				if err == nil || process.IsRunning() || process.IsControlReady() {
					t.Fatalf("failed preparation did not stop the child: running=%t, error=%v", process.IsRunning(), err)
				}
				if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
					conn.Close()
					t.Fatal("failed preparation left a business listener")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !process.IsControlReady() {
				t.Fatal("activated core did not become available to Runtime")
			}
			conn, err := net.DialTimeout("tcp", address, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			payload := bytes.Repeat([]byte{0x25}, 256)
			if _, err := conn.Write(payload); err != nil {
				t.Fatal(err)
			}
			reply := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, reply); err != nil || !bytes.Equal(reply, payload) {
				t.Fatalf("activated managed listener: %x, error=%v", reply, err)
			}
			api, err := DialClientPolicy(ctx, socket, "managed-process")
			if err != nil {
				t.Fatal(err)
			}
			defer api.Close()
			client, err := api.GetClient(ctx, "owner")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "quota-window" && (client.Usage.Remainder != 500000 || client.Policy.QuotaBaselineBytes != 100 || client.Policy.QuotaBaselineRemainder != 500000) {
				t.Fatalf("process bootstrap lost quota baseline: %+v", client)
			}
			if client.Usage.RawUpload != 256 || client.Usage.RawDownload != 356 || client.Usage.BilledBytes != 612 {
				t.Fatalf("activation lost the historical seed: %+v", client.Usage)
			}
		})
	}
}

func TestManagedProcessRejectsUnmodifiedCoreBeforeBusinessTraffic(t *testing.T) {
	binary := os.Getenv("XRAY_UPSTREAM_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_UPSTREAM_E2E_BINARY to an unmodified upstream binary")
	}
	dir, err := os.MkdirTemp("", "panel-upstream-reject-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService"]},"clientPolicy":{"stateFile":%q,"instanceId":"expected","policies":[]},"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":9,"clientId":"owner"}}],"outbounds":[{"protocol":"freedom"}]}`, filepath.Join(dir, "control.sock"), filepath.Join(dir, "state.db"), port)
	var config Config
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	process := NewTestProcess(&config, filepath.Join(dir, "bootstrap.json"))
	t.Cleanup(func() { _ = process.Stop() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = process.StartManaged(ctx, func(context.Context, *ClientPolicyAPI) error {
		t.Error("unmodified core was allowed to prepare managed users")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "negotiation") && !errors.Is(err, ErrClientPolicyCapability) {
		t.Fatalf("upstream core was not rejected during negotiation: %v", err)
	}
	if process.IsRunning() || process.IsControlReady() {
		t.Fatal("rejected upstream child is still running")
	}
	if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
		conn.Close()
		t.Fatal("unmodified upstream opened the managed business listener")
	}
}
