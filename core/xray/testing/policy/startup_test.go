package policy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/xtls/xray-core/app/clientpolicy"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf"
)

var errInjectedStart = errors.New("injected final feature startup failure")

type failingStartupFeature struct{}

func (*failingStartupFeature) Type() interface{} { return (*failingStartupFeature)(nil) }
func (*failingStartupFeature) Start() error      { return errInjectedStart }
func (*failingStartupFeature) Close() error      { return nil }

func TestFailedCoreStartReleasesListenersAndKeepsCommittedPolicy(t *testing.T) {
	for _, failure := range []string{"control-address-in-use", "last-feature-fails", "multiple-committers"} {
		t.Run(failure, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "core-start-rollback-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			state, socket := filepath.Join(dir, "policy.db"), filepath.Join(dir, "control.sock")
			if err := clientpolicy.CreateStore(state, "rollback-test"); err != nil {
				t.Fatal(err)
			}
			engine, err := clientpolicy.OpenPersistentEngine(state, "rollback-test")
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Initialize(clientpolicy.Policy{ClientID: "owner", Version: 1, Enabled: true, Multiplier: 1000000, BurstBytes: 65536}, clientpolicy.Usage{RawUpload: 7, BilledBytes: 7}); err != nil {
				t.Fatal(err)
			}
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			if failure == "control-address-in-use" {
				occupied, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				defer occupied.Close()
			}
			listenPort := port(t)
			raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1"]},"clientPolicy":{"stateFile":%q,"instanceId":"rollback-test","policies":[{"clientId":"owner","version":2,"enabled":false,"multiplierMicros":2000000,"burstBytes":65536}]},"inbounds":[{"tag":"pending","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":9,"clientId":"owner"}}],"outbounds":[{"protocol":"freedom"}]}`, socket, state, listenPort)
			var config conf.Config
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			built, err := config.Build()
			if err != nil {
				t.Fatal(err)
			}
			if failure == "multiple-committers" {
				secondState := filepath.Join(dir, "second.db")
				if err := clientpolicy.CreateStore(secondState, "second"); err != nil {
					t.Fatal(err)
				}
				built.App = append(built.App, serial.ToTypedMessage(&clientpolicy.Config{StateFile: secondState, InstanceId: "second"}))
			}
			instance, err := core.New(built)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = instance.Close() })
			if failure == "last-feature-fails" {
				if err := instance.AddFeature(&failingStartupFeature{}); err != nil {
					t.Fatal(err)
				}
			}
			err = instance.Start()
			if err == nil || failure == "last-feature-fails" && !errors.Is(err, errInjectedStart) {
				t.Fatalf("unexpected startup result: %v", err)
			}
			if instance.IsRunning() {
				t.Error("failed core still reports running")
			}
			probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
			if err != nil {
				t.Errorf("failed startup leaked business listener: %v", err)
			} else {
				probe.Close()
			}
			reopened, err := clientpolicy.OpenPersistentEngine(state, "rollback-test")
			if err != nil {
				t.Fatalf("failed startup retained durable store lock: %v", err)
			}
			defer reopened.Close()
			policy, usage, err := reopened.GetClient("owner")
			if err != nil {
				t.Fatal(err)
			}
			if policy.Version != 1 || !policy.Enabled || policy.Multiplier != 1000000 || usage.Usage.RawUpload != 7 || usage.Usage.BilledBytes != 7 {
				t.Fatalf("failed startup changed committed policy or usage: %+v, %+v", policy, usage)
			}
			if failure == "control-address-in-use" {
				conn, err := net.Dial("unix", socket)
				if err != nil {
					t.Fatalf("rollback removed another owner's socket: %v", err)
				}
				conn.Close()
			} else if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed startup retained control socket: %v", err)
			}
		})
	}
}
