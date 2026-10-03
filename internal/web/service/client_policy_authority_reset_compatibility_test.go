package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

func TestManagedAuthorityResetAppliedCompatibilityPreservesOriginalTime(t *testing.T) {
	for _, laterReset := range []bool{false, true} {
		t.Run(map[bool]string{false: "original-window", true: "later-window"}[laterReset], func(t *testing.T) {
			svc, _, client, _ := setupManagedActivationService(t)
			db := database.GetDB()
			if err := db.Model(client).Update("traffic_reset", "daily").Error; err != nil {
				t.Fatal(err)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			operation, err := captureClientTrafficResetBatch(ctx, "all", nil, "historical-applied-no-preparation")
			if err != nil {
				t.Fatal(err)
			}
			// Execute the prior pipeline: original capture and real per-client core
			// acknowledgement, with no operation-level preparation/completion.
			_, _, effects, err := func() (int, bool, *trafficResetLegacyEffects, error) {
				lock.Lock()
				defer lock.Unlock()
				return (&ClientService{}).applyTrafficResetBatchLocked(ctx, operation, db, nil, nil)
			}()
			if err != nil {
				t.Fatal(err)
			}
			effects.apply(ctx, &InboundService{})
			var original model.ClientPolicyReset
			if err := db.First(&original, "client_id = ? AND request_id = ?", client.StableID, "batch:"+authorityResetSnapshotDigest(operation.RequestID)).Error; err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
			if laterReset {
				if err := ResetLocalClientPolicy(ctx, client.StableID, "later-legitimate-reset"); err != nil {
					t.Fatal(err)
				}
			}
			// Existing per-client recovery uses the protected reset row's
			// CreatedAt. Establish that historical cold-recovery baseline first;
			// the new operation retry must not advance it to retry wall time.
			cfg := managedAuthorityForProcess(currentXrayProcess()).config
			if err := svc.StopXray(); err != nil {
				t.Fatal(err)
			}
			if err := recoverAuthorityDesiredState(ctx, &cfg); err != nil {
				t.Fatal(err)
			}
			if err := svc.RestartXray(true); err != nil {
				t.Fatal(err)
			}
			var before model.ClientTrafficResetTime
			if err := db.First(&before, "client_id = ?", client.StableID).Error; err != nil {
				t.Fatal(err)
			}
			owner := managedAuthorityForProcess(currentXrayProcess())
			account, err := owner.state.Journal.Account(client.StableID)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := (&ClientService{}).ResetAllTrafficsWithRequest(ctx, operation.RequestID); err != nil {
					t.Fatal(err)
				}
			}
			cfg = owner.config
			if err := svc.StopXray(); err != nil {
				t.Fatal(err)
			}
			if err := recoverAuthorityDesiredState(ctx, &cfg); err != nil {
				t.Fatal(err)
			}
			var after model.ClientTrafficResetTime
			if err := db.First(&after, "client_id = ?", client.StableID).Error; err != nil || after != before {
				t.Fatalf("historical retry/cold recovery changed original or later reset time: before=%+v after=%+v err=%v", before, after, err)
			}
			lock.Lock()
			state, err := resetCaptureStateLocked(db)
			lock.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			defer state.Journal.Close()
			key := authorityResetRequestKey(operation.RequestID)
			if _, err := state.Journal.LookupResetPreparation(key); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("historical acknowledged operation acquired an invented preparation: %v", err)
			}
			if _, err := state.Journal.LookupResetCompletion(key); !errors.Is(err, policyauthority.ErrNotFound) {
				t.Fatalf("historical operation acquired an unsupported completion: %v", err)
			}
			retained, err := state.Journal.Account(client.StableID)
			if err != nil || account.Policy != retained.Policy || account.Usage != retained.Usage || account.WindowBaseline != retained.WindowBaseline {
				t.Fatalf("historical retry moved managed business state: %v", err)
			}
			// A subsequent calendar boundary must remain due after the original
			// reset, while a legitimate later manual reset still suppresses it.
			due := model.ClientTrafficResetBatch{TargetsJSON: operation.TargetsJSON, ScheduledAt: original.CreatedAt + 1}
			eligible, err := scheduledResetEligibleClients(db, []model.ClientRecord{*client}, due, []string{client.StableID})
			want := 1
			if laterReset {
				want = 0
			}
			if err != nil || len(eligible) != want {
				t.Fatalf("historical retry changed due calendar eligibility: got=%d want=%d err=%v", len(eligible), want, err)
			}
		})
	}
}
