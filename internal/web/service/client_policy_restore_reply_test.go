package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"github.com/xtls/xray-core/infra/conf"
)

type restoreDelayedLedgerRuntime struct {
	*panelruntime.Local
	started, release chan struct{}
	page             *command.LedgerPage
}

func (r *restoreDelayedLedgerRuntime) ReadManagedLedger(context.Context, *xray.Process, uint64, bool) (*command.Capabilities, *command.LedgerPage, error) {
	close(r.started)
	<-r.release
	return &command.Capabilities{InstanceId: "core-a", Epoch: 1, ApiVersion: 1}, r.page, nil
}

func TestClientPolicyDelayedLedgerReplyCannotCrossDatabaseReplacement(t *testing.T) {
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	setupPolicyLedgerDB(t)
	if err := BindClientPolicySource("local", "core-a", 1); err != nil {
		t.Fatal(err)
	}
	id := policyLedgerClient(t, "restored-canonical-account", 100, 200)
	before := policyLedgerTotal(t, id)
	dir := t.TempDir()
	restored := filepath.Join(dir, "restored.db")
	if err := database.BackupSQLite(restored); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(&conf.ClientPolicyConfig{InstanceID: "core-a"})
	if err != nil {
		t.Fatal(err)
	}
	process := xray.NewTestProcess(&xray.Config{ClientPolicy: config}, filepath.Join(dir, "unstarted-runtime.json"))
	rt := &restoreDelayedLedgerRuntime{
		Local:   panelruntime.NewLocal(panelruntime.LocalDeps{}),
		started: make(chan struct{}), release: make(chan struct{}),
		page: policyLedgerPage(id, 1, 110, 220, 330),
	}
	manager := panelruntime.NewManager(panelruntime.LocalDeps{})
	manager.SetLocalRuntimeOverride(rt)
	oldManager := panelruntime.GetManager()
	panelruntime.SetManager(manager)
	t.Cleanup(func() { panelruntime.SetManager(oldManager) })
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(rt.release) })
	result := make(chan error, 1)
	go func() { result <- pollLocalClientPolicyLedger(context.Background(), process) }()
	waitTrafficWriterSignal(t, rt.started, "ledger read did not reach the runtime")
	// Both snapshots deliberately have the same client/source UUID, epoch and
	// cursor. Existing epoch checks cannot identify this old response.
	if err := database.InitDB(restored); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(rt.release) })
	if err := waitTrafficWriterErr(t, result); !errors.Is(err, ErrDatabaseReplaced) {
		t.Errorf("old runtime response did not reject replacement: %v", err)
	}
	if got := policyLedgerTotal(t, id); got != before {
		t.Errorf("old response settled into restored lifetime totals: before=%+v after=%+v", before, got)
	}
	if cursor, err := ClientPolicyLedgerCursor("core-a"); err != nil || cursor != 0 {
		t.Errorf("old response advanced restored cursor: cursor=%d err=%v", cursor, err)
	}
}
