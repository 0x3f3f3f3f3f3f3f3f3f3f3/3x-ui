package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
	panelxray "github.com/mhsanaei/3x-ui/v3/internal/xray"
	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"gorm.io/gorm"
)

type authorityMigrationSnapshot struct {
	Seeds   []policyauthority.Seed
	Deleted []string
	Records []policyauthority.MigrationRecord
}

func captureAuthorityMigration(ctx context.Context, owner *databaseRestoreOwner, stateFile, instanceID string) (authorityMigrationSnapshot, error) {
	var captured authorityMigrationSnapshot
	if ctx == nil {
		return captured, ErrClientPolicyLedger
	}
	if err := ctx.Err(); err != nil {
		return captured, err
	}
	if owner == nil || activeDatabaseRestore.Load() != owner || owner.lease == nil {
		return captured, ErrDatabaseRestoreInProgress
	}
	if owner.source != database.GetDB() {
		return captured, database.ErrDatabaseReplaced
	}
	if process := currentXrayProcess(); process != nil && process.IsRunning() {
		return captured, ErrDatabaseRestoreInProgress
	}
	core, err := clientpolicy.InspectPolicyStore(stateFile, instanceID)
	if err != nil {
		return captured, err
	}
	for _, r := range core.Clients {
		if r.HasAuthority {
			return captured, ErrClientPolicyLedger
		}
	}
	err = runSerializedTxContextForDatabase(owner.lease.Context(ctx), owner.source, func(tx *gorm.DB) error {
		return captureAuthorityMigrationTx(ctx, tx, core, &captured)
	})
	if err != nil {
		return authorityMigrationSnapshot{}, err
	}
	return captured, nil
}

func captureAuthorityMigrationTx(ctx context.Context, tx *gorm.DB, core clientpolicy.OfflinePolicySnapshot, out *authorityMigrationSnapshot) error {
	var projected int64
	if err := tx.Model(&model.ClientPolicyAuthorityProjection{}).Count(&projected).Error; err != nil {
		return err
	}
	if projected != 0 {
		return ErrClientPolicyLedger
	}
	var sources []model.ClientPolicySource
	if err := tx.Find(&sources).Error; err != nil {
		return err
	}
	var source *model.ClientPolicySource
	for i := range sources {
		s := &sources[i]
		if s.NodeKey == "local" {
			source = s
		} else if s.Epoch != 0 || s.Sequence != 0 {
			return ErrClientPolicyLedger
		}
	}
	if source == nil || source.InstanceID != core.InstanceID || source.Epoch < 0 || source.Sequence < 0 || core.Epoch > math.MaxInt64 || core.Sequence > math.MaxInt64 || uint64(source.Epoch) > core.Epoch || uint64(source.Sequence) > core.Sequence || source.HandoffBootID != "" {
		return ErrClientPolicyLedger
	}
	var receipts []model.ClientPolicyReceipt
	if err := tx.Find(&receipts).Error; err != nil {
		return err
	}
	prior := make(map[string]model.ClientPolicyReceipt, len(receipts))
	for _, r := range receipts {
		if r.InstanceID != core.InstanceID || prior[r.ClientID].ClientID != "" {
			return ErrClientPolicyLedger
		}
		prior[r.ClientID] = r
	}
	stored := make(map[string]clientpolicy.OfflineClientSnapshot, len(core.Clients))
	ordered := append([]clientpolicy.OfflineClientSnapshot(nil), core.Clients...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Sequence < ordered[j].Sequence })
	for _, r := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		if id, err := uuid.Parse(r.Policy.ClientID); err != nil || id.String() != r.Policy.ClientID {
			return ErrClientPolicyLedger
		}
		stored[r.Policy.ClientID] = r
		old, exists := prior[r.Policy.ClientID]
		if !exists {
			continue
		}
		record := &command.LedgerRecord{InstanceId: core.InstanceID, ClientId: r.Policy.ClientID, Epoch: r.Epoch, Sequence: r.Sequence, PolicyVersion: r.Policy.Version, FirstUsedAt: r.FirstUsedAt, Usage: &command.Usage{RawUpload: r.Usage.RawUpload, RawDownload: r.Usage.RawDownload, BilledBytes: r.Usage.BilledBytes, Remainder: r.Usage.Remainder}, UncertainBytes: r.UncertainBytes, ReservedBytes: r.ReservedBytes, Revoked: r.Revoked}
		if err := validateLedgerPage(core.InstanceID, core.Epoch, 0, &command.LedgerPage{Records: []*command.LedgerRecord{record}, NextSequence: r.Sequence}); err != nil {
			return err
		}
		if old.Sequence < 0 || old.Epoch < 0 || uint64(old.Sequence) > r.Sequence || uint64(old.Epoch) > r.Epoch {
			return ErrClientPolicyLedger
		}
		if uint64(old.Sequence) == r.Sequence {
			if old.RawUpload != int64(r.Usage.RawUpload) || old.RawDownload != int64(r.Usage.RawDownload) || old.BilledBytes != int64(r.Usage.BilledBytes) || old.Remainder != int64(r.Usage.Remainder) || old.UncertainBytes != int64(r.UncertainBytes) || old.ReservedBytes != int64(r.ReservedBytes) || old.Revoked != r.Revoked || old.PolicyVersion != int64(r.Policy.Version) || old.FirstUsedAt != r.FirstUsedAt {
				return ErrClientPolicyLedger
			}
		} else if err := settleClientPolicyReceipt(tx, record); err != nil {
			return err
		}
	}
	for id, r := range prior {
		if _, found := stored[id]; !found && r.Sequence > 0 && !r.DeletionAbsent {
			return ErrClientPolicyLedger
		}
	}
	source.Sequence = int64(core.Sequence)
	source.Epoch = int64(core.Epoch)
	if err := tx.Save(source).Error; err != nil {
		return err
	}
	var clients []model.ClientRecord
	if err := tx.Select("id", "stable_id", "email", "enable", "total_gb", "expiry_time", "desired_policy_version", "policy_fingerprint", "policy_upload_bytes_per_second", "policy_download_bytes_per_second", "policy_multiplier").Limit(100001).Order("stable_id").Find(&clients).Error; err != nil {
		return err
	}
	if len(clients) > 100000 {
		return ErrClientPolicyLedger
	}
	clientByID := make(map[string]model.ClientRecord, len(clients))
	clientIDs := make([]string, 0, len(clients))
	for _, c := range clients {
		clientByID[c.StableID] = c
		clientIDs = append(clientIDs, c.StableID)
	}
	for _, batch := range chunkStrings(clientIDs, 1000) {
		if err := validateLocalClientPolicyResetScope(tx, batch); err != nil {
			return err
		}
	}
	for id := range stored {
		if clientByID[id].StableID != "" && prior[id].ClientID == "" {
			return ErrClientPolicyLedger
		}
	}
	totals, err := migrationRows[model.ClientPolicyTotal](tx, "totals", func(r model.ClientPolicyTotal) string { return r.ClientID }, out)
	if err != nil {
		return err
	}
	totalByID := make(map[string]model.ClientPolicyTotal, len(totals))
	for _, r := range totals {
		totalByID[r.ClientID] = r
	}
	receipts, err = migrationRows[model.ClientPolicyReceipt](tx, "receipts", func(r model.ClientPolicyReceipt) string { return r.InstanceID + "/" + r.ClientID }, out)
	if err != nil {
		return err
	}
	receiptByID := make(map[string]model.ClientPolicyReceipt, len(receipts))
	for _, r := range receipts {
		receiptByID[r.ClientID] = r
	}
	resets, err := migrationRows[model.ClientPolicyReset](tx, "resets", func(r model.ClientPolicyReset) string { return r.ClientID + "/" + r.RequestID }, out)
	if err != nil {
		return err
	}
	latest := make(map[string]*model.ClientPolicyReset)
	for i := range resets {
		r := &resets[i]
		if err := validateClientPolicyReset(r); err != nil {
			return err
		}
		if old := latest[r.ClientID]; old == nil || old.Id < r.Id {
			latest[r.ClientID] = r
		}
	}
	tombstones, err := migrationRows[model.ClientPolicyTombstone](tx, "tombstones", func(r model.ClientPolicyTombstone) string { return r.ClientID }, out)
	if err != nil {
		return err
	}
	deleted := make(map[string]bool, len(tombstones))
	for _, r := range tombstones {
		deleted[r.ClientID] = true
	}
	for id, r := range stored {
		if r.Revoked || clientByID[id].StableID == "" {
			deleted[id] = true
		}
	}
	for id := range totalByID {
		if clientByID[id].StableID == "" {
			deleted[id] = true
		}
	}
	if _, err := migrationRows[model.ClientPolicySource](tx, "sources", func(r model.ClientPolicySource) string { return r.InstanceID }, out); err != nil {
		return err
	}
	if _, err := migrationRows[model.ClientTrafficResetTime](tx, "reset-times", func(r model.ClientTrafficResetTime) string { return r.ClientID }, out); err != nil {
		return err
	}
	if _, err := migrationRows[model.ClientTrafficResetBatch](tx, "reset-batches", func(r model.ClientTrafficResetBatch) string { return r.RequestID }, out); err != nil {
		return err
	}
	legacy, err := migrationRows[panelxray.ClientTraffic](tx, "legacy-traffic", func(r panelxray.ClientTraffic) string { return r.Email }, out)
	if err != nil {
		return err
	}
	legacyByEmail := make(map[string]panelxray.ClientTraffic, len(legacy))
	for _, r := range legacy {
		legacyByEmail[r.Email] = r
	}
	ids := make(map[string]bool, len(clients)+len(deleted))
	for id := range clientByID {
		ids[id] = true
	}
	for id := range deleted {
		ids[id] = true
	}
	orderedIDs := make([]string, 0, len(ids))
	for id := range ids {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Strings(orderedIDs)
	for _, id := range orderedIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
			return ErrClientPolicyLedger
		}
		client := clientByID[id]
		row := stored[id]
		receipt := receiptByID[id]
		total, exists := totalByID[id]
		if !exists {
			traffic := legacyByEmail[client.Email]
			if traffic.Up < 0 || traffic.Down < 0 || traffic.Up > math.MaxInt64-traffic.Down {
				return ErrClientPolicyLedger
			}
			total = model.ClientPolicyTotal{ClientID: id, RawUpload: traffic.Up, RawDownload: traffic.Down, BilledBytes: traffic.Up + traffic.Down}
			if row.Policy.ClientID != "" {
				total.RawUpload = int64(row.Usage.RawUpload)
				total.RawDownload = int64(row.Usage.RawDownload)
				total.BilledBytes = int64(row.Usage.BilledBytes)
				total.UncertainBytes = int64(row.UncertainBytes)
				receipt.Remainder = int64(row.Usage.Remainder)
				receipt.ReservedBytes = int64(row.ReservedBytes)
			}
		}
		if total.RawUpload < 0 || total.RawDownload < 0 || total.BilledBytes < 0 || total.UncertainBytes < 0 || receipt.Remainder < 0 || receipt.Remainder >= 1000000 || receipt.ReservedBytes < 0 {
			return ErrClientPolicyLedger
		}
		if receipt.ClientID != "" && (total.RawUpload < receipt.RawUpload || total.RawDownload < receipt.RawDownload || total.BilledBytes < receipt.BilledBytes || total.UncertainBytes < receipt.UncertainBytes) {
			return ErrClientPolicyLedger
		}
		policy := row.Policy
		fingerprint := client.PolicyFingerprint
		if client.StableID != "" {
			_, fingerprint, err = fingerprintClientPolicy(client, latest[id])
			if err != nil {
				return err
			}
			policy, err = prepareClientPolicyRecord(tx, client, latest[id])
			if err != nil {
				return err
			}
		}
		if policy.ClientID == "" {
			policy = clientpolicy.Policy{ClientID: id, Version: 1, Multiplier: 1000000, BurstBytes: 65536}
		}
		if policy.Validate() != nil || total.BilledBytes > math.MaxInt64-total.UncertainBytes || uint64(total.UncertainBytes) > math.MaxInt64-uint64(receipt.ReservedBytes) || total.BilledBytes > math.MaxInt64-total.UncertainBytes-receipt.ReservedBytes {
			return ErrClientPolicyLedger
		}
		baseBill, baseRem, baseUncertain := int64(0), int64(0), int64(0)
		window := "initial:" + id
		if reset := latest[id]; reset != nil {
			if reset.InstanceID != core.InstanceID || reset.BilledBytes > total.BilledBytes || reset.UncertainBytes > total.UncertainBytes || reset.PolicyVersion > int64(policy.Version) {
				return ErrClientPolicyLedger
			}
			baseBill, baseRem, baseUncertain = reset.BilledBytes, reset.Remainder, reset.UncertainBytes
			digest := sha256.Sum256([]byte(id + "/" + reset.RequestID))
			window = "reset:" + hex.EncodeToString(digest[:])
		}
		bill, rem := total.BilledBytes-baseBill, receipt.Remainder-baseRem
		if rem < 0 {
			bill--
			rem += 1000000
		}
		if bill < 0 {
			return ErrClientPolicyLedger
		}
		direction := func(rate uint64) policyauthority.Direction {
			if rate == 0 {
				return policyauthority.Direction{Unlimited: true}
			}
			return policyauthority.Direction{Rate: rate, Burst: policy.BurstBytes}
		}
		seed := policyauthority.Seed{ClientID: id, Policy: policyauthority.Policy{WindowID: window, Version: policy.Version, QuotaUnlimited: policy.QuotaBytes == 0, QuotaBytes: policy.QuotaBytes, Upload: direction(policy.UploadRate), Download: direction(policy.DownloadRate)}, Usage: policyauthority.Usage{RawUpload: uint64(total.RawUpload), RawDownload: uint64(total.RawDownload), BilledBytes: uint64(total.BilledBytes), Remainder: uint64(receipt.Remainder)}, WindowUsed: uint64(bill), WindowRemainder: uint64(rem), FrozenBilled: uint64(total.UncertainBytes - baseUncertain + receipt.ReservedBytes)}
		out.Seeds = append(out.Seeds, seed)
		if deleted[id] {
			out.Deleted = append(out.Deleted, id)
		}
		if err := appendMigrationRecord(out, "client-policy", id, struct {
			Policy      clientpolicy.Policy
			Fingerprint string
		}{policy, fingerprint}); err != nil {
			return err
		}
	}
	for _, r := range core.Clients {
		if err := appendMigrationRecord(out, "execution-clients", r.Policy.ClientID, r); err != nil {
			return err
		}
	}
	return appendMigrationRecord(out, "execution-state", core.InstanceID, struct {
		InstanceID      string
		Epoch, Sequence uint64
	}{core.InstanceID, core.Epoch, core.Sequence})
}

func migrationRows[T any](tx *gorm.DB, kind string, key func(T) string, out *authorityMigrationSnapshot) ([]T, error) {
	var rows []T
	if err := tx.Limit(1000001).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > 1000000 {
		return nil, ErrClientPolicyLedger
	}
	for _, r := range rows {
		if err := appendMigrationRecord(out, kind, key(r), r); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func appendMigrationRecord(out *authorityMigrationSnapshot, kind, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("migration evidence: %w", err)
	}
	out.Records = append(out.Records, policyauthority.MigrationRecord{Kind: kind, Key: key, Value: raw})
	return nil
}
