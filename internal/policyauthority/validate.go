package policyauthority

import (
	"encoding/json"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"sort"
)

// Open checks every stored issuance and recomputes held quota/rate/burst and
// cumulative history. A restored/corrupt projection is not a new allowance.
func (j *Journal) validate(tx *bolt.Tx) error {
	for _, name := range bucketNames {
		limit := maxRecords
		if name == "migration" {
			limit = maxMigrationRecords
		}
		if tx.Bucket([]byte(name)) == nil || tx.Bucket([]byte(name)).Stats().KeyN > limit {
			return ErrJournal
		}
	}
	var meta metadata
	if err := get(tx, "metadata", "state", &meta); err != nil || meta.Schema != 4 {
		return ErrJournal
	}
	if meta.Identity != j.id {
		return ErrIdentity
	}
	if meta.MigrationSource != "" || meta.SnapshotDigest != "" {
		if !key(meta.MigrationSource) || !validSnapshotDigest(meta.SnapshotDigest) {
			return ErrJournal
		}
	}
	if err := validateMigrationRecords(tx, meta.MigrationDigest); err != nil {
		return err
	}
	accounts := make(map[string]Account)
	calculated := make(map[string]Account)
	policies := make(map[string]Policy)
	if err := tx.Bucket([]byte("accounts")).ForEach(func(k, v []byte) error {
		var account Account
		if len(v) > maxRecordBytes || json.Unmarshal(v, &account) != nil || !validSeed(account.Seed) || !validPolicy(account.Policy) || string(k) != account.Seed.ClientID || !validUsage(account.Usage) || account.Revision == 0 || account.WindowRemainder >= fractionScale || account.WindowBaselineRemainder >= fractionScale || account.WindowBaseRemainder >= fractionScale || account.HeldRemainder >= fractionScale || !bounded(account.Revision, account.WindowUsed, account.WindowBaseline, account.WindowBaseUsed, account.FrozenBilled, account.HeldCapacity, account.UploadHeld.Rate, account.UploadHeld.Burst, account.DownloadHeld.Rate, account.DownloadHeld.Burst, account.UploadUnlimitedHeld, account.DownloadUnlimitedHeld) {
			return ErrJournal
		}
		accounts[string(k)] = account
		calculated[string(k)] = initialAccount(account.Seed)
		policies[compound(string(k), fmt.Sprint(account.Seed.Policy.Version))] = account.Seed.Policy
		return nil
	}); err != nil {
		return err
	}
	changes := make(map[string][]Change)
	if err := tx.Bucket([]byte("changes")).ForEach(func(k, v []byte) error {
		var change Change
		if len(v) > maxRecordBytes || json.Unmarshal(v, &change) != nil || change.Request.Identity != j.id || !key(change.Request.RequestID) || string(k) != compound(change.Request.ClientID, change.Request.RequestID) || !validPolicy(change.Request.Policy) || !validPolicy(change.PreviousPolicy) || !validUsage(change.UsageBoundary) || change.WindowRemainderBefore >= fractionScale || !bounded(change.WindowUsedBefore, change.FrozenBefore) {
			return ErrJournal
		}
		if _, exists := accounts[change.Request.ClientID]; !exists {
			return ErrJournal
		}
		changes[change.Request.ClientID] = append(changes[change.Request.ClientID], change)
		return nil
	}); err != nil {
		return err
	}
	for id, account := range accounts {
		total := calculated[id]
		priorUsage := account.Seed.Usage
		windows := map[string]bool{total.Policy.WindowID: true}
		history := changes[id]
		sort.Slice(history, func(i, j int) bool { return history[i].Request.Policy.Version < history[j].Request.Policy.Version })
		for _, change := range history {
			if change.PreviousPolicy != total.Policy || change.Request.ExpectedVersion != total.Policy.Version || change.Request.Policy.Version <= total.Policy.Version || change.UsageBoundary.RawUpload < priorUsage.RawUpload || change.UsageBoundary.RawDownload < priorUsage.RawDownload || usageAmount(change.UsageBoundary).less(usageAmount(priorUsage)) || change.UsageBoundary.RawUpload > account.Usage.RawUpload || change.UsageBoundary.RawDownload > account.Usage.RawDownload || usageAmount(account.Usage).less(usageAmount(change.UsageBoundary)) || change.FrozenBefore != total.FrozenBilled {
				return ErrJournal
			}
			windowDelta, err := subtractAmounts(usageAmount(change.UsageBoundary), amount{total.WindowBaseline, total.WindowBaselineRemainder})
			if err != nil {
				return ErrJournal
			}
			windowBefore, err := addAmounts(amount{total.WindowBaseUsed, total.WindowBaseRemainder}, windowDelta)
			if err != nil || windowBefore != (amount{change.WindowUsedBefore, change.WindowRemainderBefore}) {
				return ErrJournal
			}
			if change.Request.Reset {
				if windows[change.Request.Policy.WindowID] {
					return ErrJournal
				}
				total.WindowBaseline, total.WindowBaseUsed, total.FrozenBilled = change.UsageBoundary.BilledBytes, 0, 0
				total.WindowBaselineRemainder, total.WindowBaseRemainder = change.UsageBoundary.Remainder, 0
			} else if change.Request.Policy.WindowID != total.Policy.WindowID {
				return ErrJournal
			}
			windows[change.Request.Policy.WindowID] = true
			total.Policy = change.Request.Policy
			policies[compound(id, fmt.Sprint(total.Policy.Version))] = total.Policy
			priorUsage = change.UsageBoundary
			if err := advanceRevision(&total); err != nil {
				return ErrJournal
			}
		}
		if account.Policy != total.Policy || account.WindowBaseline != total.WindowBaseline || account.WindowBaselineRemainder != total.WindowBaselineRemainder || account.WindowBaseUsed != total.WindowBaseUsed || account.WindowBaseRemainder != total.WindowBaseRemainder || account.FrozenBilled != total.FrozenBilled {
			return ErrJournal
		}
		calculated[id] = total
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
		if len(v) > maxRecordBytes || json.Unmarshal(v, &grant) != nil || string(k) != grant.GrantID || grant.Sequence == 0 || grant.Sequence > meta.Sequence || sequences[grant.Sequence] || grant.GrantID != fmt.Sprintf("%s:%d", j.id.AuthorityID, grant.Sequence) || grant.Request.Binding.Identity != j.id || !validBoot(grant.Request.Binding.NodeBoot) || !key(grant.Request.RequestID) || !key(grant.Request.ChallengeID) || grant.Request.LeaseDuration <= 0 || grant.Request.LeaseDuration > MaxLeaseDuration || grant.Request.Capacity == 0 || !bounded(grant.Request.Capacity, grant.Sequence, grant.ReportSequence, grant.ReportCount) || !validUsage(grant.Usage) || (amount{grant.Request.Capacity, 0}).less(usageAmount(grant.Usage)) || !validDirection(grant.Request.Upload) || !validDirection(grant.Request.Download) || grant.ReportCount > grant.ReportSequence || (grant.ReportCount == 0) != (grant.ReportSequence == 0) || grant.Sealed && grant.ReportSequence == 0 || grant.ReportSequence == 0 && grant.Usage != (Usage{}) {
			return ErrJournal
		}
		sequences[grant.Sequence] = true
		if grant.Sequence > maximum {
			maximum = grant.Sequence
		}
		account, exists := accounts[grant.Request.Binding.ClientID]
		node := nodes[grant.Request.Binding.NodeBoot.NodeID]
		historical, knownPolicy := policies[compound(grant.Request.Binding.ClientID, fmt.Sprint(grant.Request.Binding.PolicyVersion))]
		if !exists || !knownPolicy || node.SourceID != grant.Request.Binding.NodeBoot.SourceID || historical.WindowID != grant.Request.Binding.WindowID || grant.RatesReleased && (grant.Sealed || node == grant.Request.Binding.NodeBoot) {
			return ErrJournal
		}
		if _, err := allocateDirection(historical.Upload, Direction{}, grant.Request.Upload); err != nil {
			return ErrJournal
		}
		if _, err := allocateDirection(historical.Download, Direction{}, grant.Request.Download); err != nil {
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
		if total.Revision, err = sum(total.Revision, 1, grant.ReportCount); err != nil {
			return ErrJournal
		}
		if grant.RatesReleased {
			if err := advanceRevision(&total); err != nil {
				return err
			}
		}
		if total.Usage, err = addUsage(total.Usage, grant.Usage); err != nil {
			return ErrJournal
		}
		if !grant.Sealed {
			remaining, err := subtractAmounts(amount{grant.Request.Capacity, 0}, usageAmount(grant.Usage))
			if err != nil {
				return ErrJournal
			}
			held, err := addAmounts(amount{total.HeldCapacity, total.HeldRemainder}, remaining)
			if err != nil {
				return ErrJournal
			}
			total.HeldCapacity, total.HeldRemainder = held.whole, held.fraction
		}
		if !grant.Sealed && !grant.RatesReleased {
			if total.UploadHeld, err = allocateDirection(Direction{Unlimited: true}, total.UploadHeld, grant.Request.Upload); err != nil {
				return ErrJournal
			}
			if total.DownloadHeld, err = allocateDirection(Direction{Unlimited: true}, total.DownloadHeld, grant.Request.Download); err != nil {
				return ErrJournal
			}
			if grant.Request.Upload.Unlimited {
				total.UploadUnlimitedHeld++
			}
			if grant.Request.Download.Unlimited {
				total.DownloadUnlimitedHeld++
			}
		}
		calculated[account.Seed.ClientID] = total
		return nil
	}); err != nil {
		return err
	}
	for id, account := range accounts {
		total := calculated[id]
		if account.Deleted {
			if err := advanceRevision(&total); err != nil {
				return ErrJournal
			}
		}
		windowDelta, err := subtractAmounts(usageAmount(total.Usage), amount{total.WindowBaseline, total.WindowBaselineRemainder})
		if err != nil {
			return ErrJournal
		}
		windowUsed, err := addAmounts(amount{total.WindowBaseUsed, total.WindowBaseRemainder}, windowDelta)
		if err != nil || account.Revision != total.Revision || account.HeldCapacity != total.HeldCapacity || account.HeldRemainder != total.HeldRemainder || account.UploadHeld != total.UploadHeld || account.DownloadHeld != total.DownloadHeld || account.UploadUnlimitedHeld != total.UploadUnlimitedHeld || account.DownloadUnlimitedHeld != total.DownloadUnlimitedHeld || account.Usage != total.Usage || (amount{account.WindowUsed, account.WindowRemainder}) != windowUsed {
			return ErrJournal
		}
	}
	if maximum != meta.Sequence || tx.Bucket([]byte("requests")).Stats().KeyN != tx.Bucket([]byte("grants")).Stats().KeyN {
		return ErrJournal
	}
	return nil
}
