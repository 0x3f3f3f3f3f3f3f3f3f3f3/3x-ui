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
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/infra/conf"
)

func migratedAuthorityFixture(t *testing.T) (string, string) {
	t.Helper()
	path, client, _ := authorityMigrationFixture(t)
	owner, err := acquireDatabaseRestore()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.release()
	if err := owner.fenceDatabase(); err != nil {
		t.Fatal(err)
	}
	state, err := migrateAuthorityWithOwner(context.Background(), owner, path, "migration-source", filepath.Join(filepath.Dir(path), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Journal.Close(); err != nil {
		t.Fatal(err)
	}
	return path, client
}

func authorityFactoryProcess(t *testing.T, path, client string) (*panelxray.Process, *conf.ClientPolicyConfig, string) {
	t.Helper()
	binary := os.Getenv("XRAY_E2E_BINARY")
	if binary == "" {
		t.Skip("set XRAY_E2E_BINARY to the demand-capable core")
	}
	dir := filepath.Dir(path)
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(binary, filepath.Join(dir, panelxray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	policies, err := PrepareClientPolicies([]string{client})
	if err != nil {
		t.Fatal(err)
	}
	state := &conf.ClientPolicyConfig{StateFile: path, InstanceID: "migration-source", Policies: policies}
	policyRaw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	probe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address, port := probe.Addr().String(), probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	raw := fmt.Sprintf(`{"log":{"loglevel":"error"},"api":{"tag":"control","listen":%q,"services":["ClientPolicyServiceV1","HandlerService"]},"clientPolicy":%s,"inbounds":[{"tag":"owned","listen":"127.0.0.1","port":%d,"protocol":"tunnel","settings":{"network":"tcp","address":"127.0.0.1","port":%d,"clientId":%q}}],"outbounds":[{"protocol":"freedom","settings":{"finalRules":[{"action":"allow","ip":["127.0.0.1"]}]}}]}`, filepath.Join(dir, "control.sock"), policyRaw, port, echo.Addr().(*net.TCPAddr).Port, client)
	var cfg panelxray.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	process := panelxray.NewTestProcess(&cfg, filepath.Join(dir, "factory.json"))
	t.Cleanup(func() { _ = process.Stop() })
	return process, state, address
}

func TestManagedAuthorityStartupUsesCommittedMigrationAndAutomaticDemand(t *testing.T) {
	path, client := migratedAuthorityFixture(t)
	process, config, address := authorityFactoryProcess(t, path, client)
	authority, err := openManagedAuthority(process, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = authority.Stop(ctx)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	local := panelruntime.NewLocal(panelruntime.LocalDeps{})
	if err := local.StartManagedProcess(ctx, process, authority.Prepare); err != nil {
		t.Fatal(err)
	}
	authorityEndpointExchange(t, address, 5)
	if err := authority.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := openAuthorityState(filepath.Join(filepath.Dir(path), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Journal.Close()
	a, err := state.Journal.Account(client)
	if err != nil || a.Usage != (policyauthority.Usage{RawUpload: 11, RawDownload: 9, BilledBytes: 27, Remainder: 500000}) || a.HeldCapacity != 0 || a.UploadHeld.Rate != 0 || a.DownloadHeld.Rate != 0 || a.WindowUsed != 18 {
		t.Fatalf("automatic managed startup settlement: %+v/%v", a, err)
	}
}

func TestManagedAuthorityMissingStateAndWrongSourceNeverReinitialize(t *testing.T) {
	path, client := migratedAuthorityFixture(t)
	process, config, _ := authorityFactoryProcess(t, path, client)
	config.InstanceID = "another-source"
	if authority, err := openManagedAuthority(process, config); !errors.Is(err, policyauthority.ErrIdentity) || authority != nil {
		t.Fatalf("wrong source opened: %v/%v", authority, err)
	}
	config.InstanceID = "migration-source"
	dir := filepath.Join(filepath.Dir(path), "authority")
	if err := os.Rename(dir, dir+".retained"); err != nil {
		t.Fatal(err)
	}
	if authority, err := openManagedAuthority(process, config); err == nil || authority != nil {
		t.Fatalf("missing state initialized: %v/%v", authority, err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary startup recreated authority: %v", err)
	}
}
