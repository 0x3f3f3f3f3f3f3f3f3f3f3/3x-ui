package policyauthority

import (
	"encoding/json"
	"fmt"
	bolt "go.etcd.io/bbolt"
)

// Open checks every stored issuance and recomputes held quota/rate/burst and
// cumulative history. A restored/corrupt projection is not a new allowance.
func (j *Journal) validate(tx *bolt.Tx) error {
	for _, name := range bucketNames {
		if tx.Bucket([]byte(name)) == nil || tx.Bucket([]byte(name)).Stats().KeyN > maxRecords {
			return ErrJournal
		}
	}
	var meta metadata
	if err := get(tx, "metadata", "state", &meta); err != nil || meta.Schema != 1 {
		return ErrJournal
	}
	if meta.Identity != j.id {
		return ErrIdentity
	}
	accounts := make(map[string]Account)
	calculated := make(map[string]Account)
	if err := tx.Bucket([]byte("accounts")).ForEach(func(k, v []byte) error {
		var account Account
		if len(v) > maxRecordBytes || json.Unmarshal(v, &account) != nil || !validSeed(account.Seed) || string(k) != account.Seed.ClientID || !validUsage(account.Usage) || !bounded(account.WindowUsed, account.HeldCapacity, account.UploadHeld.Rate, account.UploadHeld.Burst, account.DownloadHeld.Rate, account.DownloadHeld.Burst) {
			return ErrJournal
		}
		accounts[string(k)] = account
		calculated[string(k)] = Account{Usage: account.Seed.Usage, WindowUsed: account.Seed.WindowUsed}
		return nil
	}); err != nil {
		return err
	}
	nodes := make(map[string]NodeBoot)
	if err := tx.Bucket([]byte("nodes")).ForEach(func(k, v []byte) error {
		var boot NodeBoot
		if len(v) > maxRecordBytes || json.Unmarshal(v, &boot) != nil || !validBoot(boot) || string(k) != boot.NodeID {
			return ErrJournal
		}
		nodes[string(k)] = boot
		return nil
	}); err != nil {
		return err
	}
	sequences := make(map[uint64]bool)
	if err := tx.Bucket([]byte("boots")).ForEach(func(k, v []byte) error {
		var boot NodeBoot
		if len(v) > maxRecordBytes || json.Unmarshal(v, &boot) != nil || !validBoot(boot) || string(k) != compound(boot.NodeID, boot.BootID) || nodes[boot.NodeID].SourceID != boot.SourceID {
			return ErrJournal
		}
		return nil
	}); err != nil {
		return err
	}
	for _, boot := range nodes {
		var recorded NodeBoot
		if err := get(tx, "boots", compound(boot.NodeID, boot.BootID), &recorded); err != nil || recorded != boot {
			return ErrJournal
		}
	}
	var maximum uint64
	if err := tx.Bucket([]byte("grants")).ForEach(func(k, v []byte) error {
		var grant Grant
		if len(v) > maxRecordBytes || json.Unmarshal(v, &grant) != nil || string(k) != grant.GrantID || grant.Sequence == 0 || grant.Sequence > meta.Sequence || sequences[grant.Sequence] || grant.GrantID != fmt.Sprintf("%s:%d", j.id.AuthorityID, grant.Sequence) || grant.Request.Binding.Identity != j.id || !validBoot(grant.Request.Binding.NodeBoot) || !key(grant.Request.RequestID) || !key(grant.Request.ChallengeID) || grant.Request.LeaseDuration <= 0 || grant.Request.LeaseDuration > MaxLeaseDuration || grant.Request.Capacity == 0 || !bounded(grant.Request.Capacity) || !validUsage(grant.Usage) || grant.Usage.BilledBytes > grant.Request.Capacity || !validDirection(grant.Request.Upload) || !validDirection(grant.Request.Download) || grant.Sealed && grant.ReportSequence == 0 || grant.ReportSequence == 0 && grant.Usage != (Usage{}) {
			return ErrJournal
		}
		sequences[grant.Sequence] = true
		if grant.Sequence > maximum {
			maximum = grant.Sequence
		}
		account, exists := accounts[grant.Request.Binding.ClientID]
		node := nodes[grant.Request.Binding.NodeBoot.NodeID]
		if !exists || node.SourceID != grant.Request.Binding.NodeBoot.SourceID || account.Seed.Policy.WindowID != grant.Request.Binding.WindowID || account.Seed.Policy.Version != grant.Request.Binding.PolicyVersion {
			return ErrJournal
		}
		var registered NodeBoot
		if err := get(tx, "boots", compound(grant.Request.Binding.NodeBoot.NodeID, grant.Request.Binding.NodeBoot.BootID), &registered); err != nil || registered != grant.Request.Binding.NodeBoot {
			return ErrJournal
		}
		requestKey := compound(grant.Request.Binding.NodeBoot.NodeID, grant.Request.Binding.ClientID, grant.Request.RequestID)
		if string(tx.Bucket([]byte("requests")).Get([]byte(requestKey))) != grant.GrantID {
			return ErrJournal
		}
		total := calculated[account.Seed.ClientID]
		var err error
		if total.Usage.RawUpload, err = sum(total.Usage.RawUpload, grant.Usage.RawUpload); err != nil {
			return ErrJournal
		}
		if total.Usage.RawDownload, err = sum(total.Usage.RawDownload, grant.Usage.RawDownload); err != nil {
			return ErrJournal
		}
		if total.Usage.BilledBytes, err = sum(total.Usage.BilledBytes, grant.Usage.BilledBytes); err != nil {
			return ErrJournal
		}
		if total.WindowUsed, err = sum(total.WindowUsed, grant.Usage.BilledBytes); err != nil {
			return ErrJournal
		}
		if !grant.Sealed {
			if total.HeldCapacity, err = sum(total.HeldCapacity, grant.Request.Capacity-grant.Usage.BilledBytes); err != nil {
				return ErrJournal
			}
			if total.UploadHeld, err = allocateDirection(account.Seed.Policy.Upload, total.UploadHeld, grant.Request.Upload); err != nil {
				return ErrJournal
			}
			if total.DownloadHeld, err = allocateDirection(account.Seed.Policy.Download, total.DownloadHeld, grant.Request.Download); err != nil {
				return ErrJournal
			}
		}
		calculated[account.Seed.ClientID] = total
		return nil
	}); err != nil {
		return err
	}
	for id, account := range accounts {
		total := calculated[id]
		if account.HeldCapacity != total.HeldCapacity || account.UploadHeld != total.UploadHeld || account.DownloadHeld != total.DownloadHeld || account.Usage != total.Usage || account.WindowUsed != total.WindowUsed {
			return ErrJournal
		}
	}
	if maximum != meta.Sequence || tx.Bucket([]byte("requests")).Stats().KeyN != tx.Bucket([]byte("grants")).Stats().KeyN {
		return ErrJournal
	}
	return nil
}
