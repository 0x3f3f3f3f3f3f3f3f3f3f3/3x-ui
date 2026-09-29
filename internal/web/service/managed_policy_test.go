package service

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyflow"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func managedPolicyClient(t *testing.T, quota int64) model.ClientRecord {
	t.Helper()
	c := model.ClientRecord{Email: uuid.NewString(), Enable: true, TotalGB: quota}
	if err := database.GetDB().Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&xray.ClientTraffic{Email: c.Email, Enable: true, Total: quota}).Error; err != nil {
		t.Fatal(err)
	}
	return c
}

func TestManagedPolicyOwnersShareSSHAdmissionAndSurvivePeerStop(t *testing.T) {
	setupConflictDB(t)
	c := managedPolicyClient(t, 24)
	ledger := database.NewClientUsageLedger(database.GetDB())
	if _, err := ledger.ChangeMultiplier(t.Context(), c.PolicyID, 1, 1500, nil); err != nil {
		t.Fatal(err)
	}
	sshManager := managedSSHRuntime()
	t.Cleanup(stopManagedSSH)
	peer := acquireManagedPolicy(database.GetDB())
	t.Cleanup(peer.Close)
	controllers := []*policyflow.Controller{sshManager.controller, peer.controller}
	for _, controller := range controllers {
		if err := controller.Configure(t.Context(), c.PolicyID, policyflow.Rates{}); err != nil {
			t.Fatal(err)
		}
	}
	flows := make([]*policyflow.Flow, policyflow.MaxFlows)
	for i := range flows {
		var err error
		flows[i], err = controllers[i%2].Open(t.Context(), c.PolicyID)
		if err != nil {
			t.Fatalf("owner %d fenced its peer's active source: %v", i%2, err)
		}
		t.Cleanup(flows[i].Close)
	}
	if f, err := peer.controller.Open(t.Context(), c.PolicyID); !errors.Is(err, policyflow.ErrTooManyFlows) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("owners received separate per-client flow allowances: %v", err)
	}
	var actual bytes.Buffer
	for i, payload := range []string{"abcd", "12345"} {
		if n, err := flows[i].Writer(policyflow.Upload, &actual).Write([]byte(payload)); err != nil || n != len(payload) {
			t.Fatalf("owner %d payload: n=%d err=%v", i, n, err)
		}
	}
	if actual.String() != "abcd12345" {
		t.Fatalf("forwarded payload %q", actual.String())
	}
	stopManagedSSH()
	if n, err := flows[1].Writer(policyflow.Download, &actual).Write([]byte("xyz")); n != 3 || err != nil {
		t.Fatalf("stopping SSH interrupted surviving owner: n=%d err=%v", n, err)
	}
	peer.Close()
	select {
	case <-flows[1].Context().Done():
	case <-time.After(time.Second):
		t.Fatal("last owner release left an active flow")
	}
	fresh := acquireManagedPolicy(database.GetDB())
	t.Cleanup(fresh.Close)
	peer.Close()
	if err := fresh.controller.Configure(t.Context(), c.PolicyID, policyflow.Rates{}); err != nil {
		t.Fatal(err)
	}
	f, err := fresh.controller.Open(t.Context(), c.PolicyID)
	if err != nil {
		t.Fatalf("repeated stale release closed the new owner: %v", err)
	}
	defer f.Close()
	var remaining bytes.Buffer
	if n, err := f.Writer(policyflow.Upload, &remaining).Write([]byte("final")); n != 4 || !errors.Is(err, database.ErrUsageQuota) || remaining.String() != "fina" {
		t.Fatalf("new owner lost durable quota: n=%d payload=%q err=%v", n, remaining.String(), err)
	}
	account, err := ledger.Read(t.Context(), c.PolicyID)
	if err != nil || account.Up != 13 || account.Down != 3 || account.Billed != 24 || account.Remainder != 0 {
		t.Fatalf("shared fractional billing across owner restart: %+v, %v", account, err)
	}
	var meters []model.ClientUsageMeter
	if err := database.GetDB().Where("policy_id = ?", c.PolicyID).Find(&meters).Error; err != nil {
		t.Fatal(err)
	}
	if len(meters) != 2 {
		t.Fatalf("expected one meter per controller lifetime, got %d", len(meters))
	}
}

func TestManagedPolicyConcurrentOwnersDoNotFenceLiveClient(t *testing.T) {
	setupConflictDB(t)
	c := managedPolicyClient(t, 0)
	root := acquireManagedPolicy(database.GetDB())
	t.Cleanup(root.Close)
	if err := root.controller.Configure(t.Context(), c.PolicyID, policyflow.Rates{}); err != nil {
		t.Fatal(err)
	}
	live, err := root.controller.Open(t.Context(), c.PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			owner := acquireManagedPolicy(database.GetDB())
			defer owner.Close()
			if err := owner.controller.Configure(t.Context(), c.PolicyID, policyflow.Rates{}); err != nil {
				t.Error(err)
				return
			}
			flow, err := owner.controller.Open(t.Context(), c.PolicyID)
			if err != nil {
				t.Error(err)
				return
			}
			defer flow.Close()
			if n, err := flow.Writer(policyflow.Upload, io.Discard).Write([]byte("x")); n != 1 || err != nil {
				t.Errorf("concurrent owner lost admission: n=%d err=%v", n, err)
			}
		})
	}
	workers.Wait()
	if n, err := live.Writer(policyflow.Download, io.Discard).Write([]byte("peer")); n != 4 || err != nil {
		t.Fatalf("transient owners retired the live client's meter: n=%d err=%v", n, err)
	}
	account, err := database.NewClientUsageLedger(database.GetDB()).Read(t.Context(), c.PolicyID)
	if err != nil || account.Up != 20 || account.Down != 4 || account.Billed != 24 {
		t.Fatalf("concurrent owners duplicated/lost accounting: %+v, %v", account, err)
	}
}

func TestManagedPolicyDatabaseReplacementKeepsIdenticalIDsIsolated(t *testing.T) {
	setupConflictDB(t)
	c := managedPolicyClient(t, 0)
	firstDB := database.GetDB()
	secondDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "replacement.db")+"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=1000&_txlock=immediate"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	if err := secondDB.AutoMigrate(&model.ClientRecord{}, &xray.ClientTraffic{}, &model.ClientUsageAccount{}, &model.ClientUsageMeter{}); err != nil {
		t.Fatal(err)
	}
	if err := secondDB.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	if err := secondDB.Create(&xray.ClientTraffic{Email: c.Email, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	first, second := acquireManagedPolicy(firstDB), acquireManagedPolicy(secondDB)
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	for i, owner := range []*managedPolicyLease{first, second} {
		if err := owner.controller.Configure(t.Context(), c.PolicyID, policyflow.Rates{}); err != nil {
			t.Fatal(err)
		}
		flow, err := owner.controller.Open(t.Context(), c.PolicyID)
		if err != nil {
			t.Fatal(err)
		}
		defer flow.Close()
		if i == 1 {
			first.Close()
		}
		payload := []byte("ab")[:i+1]
		if n, err := flow.Writer(policyflow.Upload, io.Discard).Write(payload); n != len(payload) || err != nil {
			t.Fatalf("database owner %d lost independent admission: n=%d err=%v", i, n, err)
		}
	}
	for i, db := range []*gorm.DB{firstDB, secondDB} {
		account, err := database.NewClientUsageLedger(db).Read(t.Context(), c.PolicyID)
		if err != nil || account.Up != int64(i+1) || account.Down != 0 || account.Billed != int64(i+1) {
			t.Fatalf("database owner %d inherited another database's usage: %+v, %v", i, account, err)
		}
	}
}

func TestManagedPolicy_Postgres(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"shared ownership", TestManagedPolicyOwnersShareSSHAdmissionAndSurvivePeerStop},
		{"concurrent owners", TestManagedPolicyConcurrentOwnersDoNotFenceLiveClient},
		{"real SSH and mieru", TestManagedPolicySSHAndMieruShareLiveDuplexRates},
	} {
		t.Run(test.name, func(t *testing.T) { managedUsagePostgresSchema(t); test.run(t) })
	}
}
