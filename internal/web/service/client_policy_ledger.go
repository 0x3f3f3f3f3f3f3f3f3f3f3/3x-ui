package service

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/xtls/xray-core/app/clientpolicy"
	command "github.com/xtls/xray-core/app/clientpolicy/command"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

var ErrClientPolicyLedger = errors.New("client policy ledger rejected inconsistent state")

func validPolicySourceKey(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}

func BindClientPolicySource(nodeKey, instanceID string, epoch uint64) error {
	if !validPolicySourceKey(nodeKey) || !validPolicySourceKey(instanceID) || epoch == 0 || epoch > math.MaxInt64 {
		return ErrClientPolicyLedger
	}
	return runSerializedTx(func(tx *gorm.DB) error {
		var source model.ClientPolicySource
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("node_key = ?", nodeKey).First(&source).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&model.ClientPolicySource{InstanceID: instanceID, NodeKey: nodeKey, Epoch: int64(epoch)}).Error
		}
		if err != nil {
			return err
		}
		if source.InstanceID != instanceID || source.Epoch > int64(epoch) {
			return ErrClientPolicyLedger
		}
		if source.HandoffBootID != "" {
			if err := checkLegacyHandoffReceipt(tx, &source); err != nil {
				return err
			}
		}
		return tx.Model(&source).Update("epoch", int64(epoch)).Error
	})
}

func ClientPolicyLedgerCursor(instanceID string) (uint64, error) {
	var source model.ClientPolicySource
	if err := database.GetDB().First(&source, "instance_id = ?", instanceID).Error; err != nil {
		return 0, err
	}
	if source.Sequence < 0 {
		return 0, ErrClientPolicyLedger
	}
	return uint64(source.Sequence), nil
}

// Prepare stores the legacy baseline once. Core initialization must use this exact seed before activation.
func PrepareClientPolicyLedger(instanceID, clientID string) (*command.Usage, error) {
	var seed *command.Usage
	err := runSerializedTx(func(tx *gorm.DB) error {
		var source model.ClientPolicySource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "instance_id = ?", instanceID).Error; err != nil {
			return err
		}
		if source.HandoffBootID != "" {
			if err := checkLegacyHandoffReceipt(tx, &source); err != nil {
				return err
			}
		}
		var client model.ClientRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&client, "stable_id = ?", clientID).Error; err != nil {
			return err
		}
		if err := rejectDeletedClientPolicies(tx, []string{clientID}); err != nil {
			return err
		}
		var receipt model.ClientPolicyReceipt
		err := tx.First(&receipt, "instance_id = ? AND client_id = ?", instanceID, clientID).Error
		if err == nil {
			if receipt.SeedUpload < 0 || receipt.SeedDownload < 0 || receipt.SeedBilled < 0 {
				return ErrClientPolicyLedger
			}
			seed = &command.Usage{RawUpload: uint64(receipt.SeedUpload), RawDownload: uint64(receipt.SeedDownload), BilledBytes: uint64(receipt.SeedBilled)}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var total model.ClientPolicyTotal
		err = tx.First(&total, "client_id = ?", clientID).Error
		seed = &command.Usage{}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var legacy xray.ClientTraffic
			if err := tx.First(&legacy, "email = ?", client.Email).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if legacy.Up < 0 || legacy.Down < 0 || legacy.Up > math.MaxInt64-legacy.Down {
				return ErrClientPolicyLedger
			}
			seed.RawUpload, seed.RawDownload, seed.BilledBytes = uint64(legacy.Up), uint64(legacy.Down), uint64(legacy.Up+legacy.Down)
			total = model.ClientPolicyTotal{ClientID: clientID, RawUpload: legacy.Up, RawDownload: legacy.Down, BilledBytes: legacy.Up + legacy.Down}
			if err := tx.Create(&total).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		receipt = model.ClientPolicyReceipt{InstanceID: instanceID, ClientID: clientID, RawUpload: int64(seed.RawUpload), RawDownload: int64(seed.RawDownload), BilledBytes: int64(seed.BilledBytes), SeedUpload: int64(seed.RawUpload), SeedDownload: int64(seed.RawDownload), SeedBilled: int64(seed.BilledBytes)}
		return tx.Create(&receipt).Error
	})
	return seed, err
}

func validateLedgerPage(instanceID string, epoch, after uint64, page *command.LedgerPage) error {
	if !validPolicySourceKey(instanceID) || epoch == 0 || epoch > math.MaxInt64 || after > math.MaxInt64 || page == nil || len(page.Records) > 1000 {
		return ErrClientPolicyLedger
	}
	last := after
	seen := make(map[string]bool, len(page.Records))
	for _, r := range page.Records {
		if r == nil || r.Usage == nil || r.InstanceId != instanceID || r.ClientId == "" || seen[r.ClientId] || r.Epoch == 0 || r.Epoch > epoch || r.Sequence <= last || r.PolicyVersion == 0 || r.FirstUsedAt < 0 || r.Usage.Remainder >= clientpolicy.MultiplierScale {
			return ErrClientPolicyLedger
		}
		for _, v := range []uint64{r.Sequence, r.PolicyVersion, r.Usage.RawUpload, r.Usage.RawDownload, r.Usage.BilledBytes, r.UncertainBytes, r.ReservedBytes} {
			if v > math.MaxInt64 {
				return ErrClientPolicyLedger
			}
		}
		if r.Usage.BilledBytes > math.MaxInt64-r.UncertainBytes || r.Usage.BilledBytes+r.UncertainBytes > math.MaxInt64-r.ReservedBytes {
			return ErrClientPolicyLedger
		}
		seen[r.ClientId], last = true, r.Sequence
	}
	if page.NextSequence != last {
		return ErrClientPolicyLedger
	}
	return nil
}

func SettleClientPolicyLedger(instanceID string, epoch, after uint64, page *command.LedgerPage) error {
	if err := validateLedgerPage(instanceID, epoch, after, page); err != nil {
		return err
	}
	return runSerializedTx(func(tx *gorm.DB) error {
		var source model.ClientPolicySource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "instance_id = ?", instanceID).Error; err != nil {
			return err
		}
		if source.Sequence < 0 || source.Epoch > int64(epoch) || int64(after) > source.Sequence {
			return ErrClientPolicyLedger
		}
		records := append([]*command.LedgerRecord(nil), page.Records...)
		sort.Slice(records, func(i, j int) bool { return records[i].ClientId < records[j].ClientId })
		for _, r := range records {
			if int64(r.Sequence) <= source.Sequence {
				continue
			}
			if err := settleClientPolicyReceipt(tx, r); err != nil {
				return err
			}
		}
		source.Epoch = int64(epoch)
		if int64(page.NextSequence) > source.Sequence {
			source.Sequence = int64(page.NextSequence)
		}
		return tx.Save(&source).Error
	})
}

func settleClientPolicyReceipt(tx *gorm.DB, r *command.LedgerRecord) error {
	var total model.ClientPolicyTotal
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&total, "client_id = ?", r.ClientId).Error; err != nil {
		return fmt.Errorf("%w: unprepared client: %w", ErrClientPolicyLedger, err)
	}
	var old model.ClientPolicyReceipt
	if err := tx.First(&old, "instance_id = ? AND client_id = ?", r.InstanceId, r.ClientId).Error; err != nil {
		return fmt.Errorf("%w: unprepared source/client: %w", ErrClientPolicyLedger, err)
	}
	if int64(r.Epoch) < old.Epoch || int64(r.Sequence) <= old.Sequence || int64(r.PolicyVersion) < old.PolicyVersion || int64(r.UncertainBytes) < old.UncertainBytes || old.Revoked && !r.Revoked {
		return ErrClientPolicyLedger
	}
	if old.FirstUsedAt < 0 || old.FirstUsedAt > 0 && r.FirstUsedAt != old.FirstUsedAt && int64(r.PolicyVersion) == old.PolicyVersion {
		return ErrClientPolicyLedger
	}
	if int64(r.Usage.BilledBytes) == old.BilledBytes && int64(r.Usage.Remainder) < old.Remainder {
		return ErrClientPolicyLedger
	}
	for _, change := range []struct {
		target   *int64
		previous int64
		next     uint64
	}{
		{&total.RawUpload, old.RawUpload, r.Usage.RawUpload},
		{&total.RawDownload, old.RawDownload, r.Usage.RawDownload},
		{&total.BilledBytes, old.BilledBytes, r.Usage.BilledBytes},
		{&total.UncertainBytes, old.UncertainBytes, r.UncertainBytes},
	} {
		if change.previous < 0 || *change.target < 0 || int64(change.next) < change.previous {
			return ErrClientPolicyLedger
		}
		delta := int64(change.next) - change.previous
		if *change.target > math.MaxInt64-delta {
			return ErrClientPolicyLedger
		}
		*change.target += delta
	}
	if total.BilledBytes > math.MaxInt64-total.UncertainBytes {
		return ErrClientPolicyLedger
	}
	old.FirstUsedAt = r.FirstUsedAt
	old.Epoch, old.Sequence, old.PolicyVersion = int64(r.Epoch), int64(r.Sequence), int64(r.PolicyVersion)
	old.RawUpload, old.RawDownload, old.BilledBytes, old.Remainder = int64(r.Usage.RawUpload), int64(r.Usage.RawDownload), int64(r.Usage.BilledBytes), int64(r.Usage.Remainder)
	old.UncertainBytes, old.ReservedBytes, old.Revoked = int64(r.UncertainBytes), int64(r.ReservedBytes), r.Revoked
	if err := tx.Save(&old).Error; err != nil {
		return err
	}
	if err := tx.Save(&total).Error; err != nil {
		return err
	}
	return activateClientPolicyFirstUse(tx, r)
}
