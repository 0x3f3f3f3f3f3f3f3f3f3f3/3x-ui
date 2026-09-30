package service

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Reading and resolving a complete shared password graph for every selected
// owner turns bulk lifecycle into quadratic work. Each bounded batch should
// validate the graph once and keep one managed acknowledgement boundary.
func TestPasswordProxyOwnerFieldsBulkBatchesSharedGraphs(t *testing.T) {
	setupPolicyLedgerDB(t)
	var owners []model.ClientRecord
	var emails []string
	var accounts []map[string]any
	for i := 0; i < 129; i++ {
		owner := passwordOwner(t, fmt.Sprintf("fields-batch-owner-%03d", i))
		owners, emails = append(owners, owner), append(emails, owner.Email)
		accounts = append(accounts, map[string]any{"user": fmt.Sprintf("wire-%03d", i), "pass": "resource-secret", "ownerClientId": owner.StableID})
		if err := database.GetDB().Create(&xray.ClientTraffic{Email: owner.Email, Enable: true, Up: int64(i + 1), Down: int64(i + 2)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	inbounds, clients := &InboundService{}, &ClientService{}
	resource, _, err := inbounds.AddInbound(&model.Inbound{Protocol: model.HTTP, Listen: "127.0.0.1", Port: 24577, Settings: passwordOwnerSettings(t, model.HTTP, accounts...)})
	if err != nil {
		t.Fatal(err)
	}
	previous := panelruntime.GetManager()
	t.Cleanup(func() { panelruntime.SetManager(previous) })
	var acknowledgements, fullGraphReads atomic.Int32
	panelruntime.SetManager(panelruntime.NewManager(panelruntime.LocalDeps{ManagedChange: func(context.Context) (bool, error) { acknowledgements.Add(1); return true, nil }}))
	const callback = "test-password-fields-batch-graph-budget"
	if err := database.GetDB().Callback().Query().After("gorm:query").Register(callback, func(query *gorm.DB) {
		if query.Statement.Table == "inbounds" && strings.Contains(query.Statement.SQL.String(), "protocol IN") {
			fullGraphReads.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.GetDB().Callback().Query().Remove(callback) })
	result, restart, err := clients.BulkSetEnable(inbounds, append(emails, emails[0]), false)
	if err != nil || restart || result.Changed != 129 || len(result.Skipped) != 0 || acknowledgements.Load() != 2 {
		t.Fatalf("bounded bulk result=%+v restart=%v error=%v acknowledgements=%d", result, restart, err, acknowledgements.Load())
	}
	if fullGraphReads.Load() > 10 {
		t.Fatalf("shared graph read once per owner instead of bounded batches: %d complete reads for 129 owners", fullGraphReads.Load())
	}
	for i, owner := range owners {
		current, err := clients.GetByID(owner.Id)
		if err != nil || current.Enable || current.StableID != owner.StableID || current.Password != owner.Password {
			t.Fatalf("bounded batch missed owner: %+v/%v", current, err)
		}
		if history := trafficOf(t, owner.Email); history.Enable || history.Up != int64(i+1) || history.Down != int64(i+2) {
			t.Fatalf("bounded batch changed history: %+v", history)
		}
	}
	if saved, err := inbounds.GetInbound(resource.Id); err != nil || saved.Settings != resource.Settings || len(linksOf(t, resource.Id)) != 129 {
		t.Fatalf("bounded batch changed account graph: %+v/%v", saved, err)
	}
}
