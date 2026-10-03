package policyauthority

import (
	bolt "go.etcd.io/bbolt"
	"strings"
	"time"
)

// ReleaseRetiredRates returns only directional shares whose former boot can no
// longer receive renewals and has waited the maximum monotonic execution lease.
// It never seals a grant or credits any of its unconfirmed billed capacity.
func (j *Journal) ReleaseRetiredRates(boot NodeBoot) error {
	if j == nil || j.closed.Load() {
		return ErrJournal
	}
	if !validBoot(boot) {
		return ErrRequest
	}
	j.retirementMu.Lock()
	defer j.retirementMu.Unlock()
	return j.update(func(tx *bolt.Tx) error {
		var recorded, current NodeBoot
		if err := get(tx, "boots", compound(boot.NodeID, boot.BootID), &recorded); err != nil || recorded != boot {
			return ErrIncarnation
		}
		if err := get(tx, "nodes", boot.NodeID, &current); err != nil || current == boot {
			return ErrIncarnation
		}
		var pending []Grant
		if err := tx.Bucket([]byte("grants")).ForEach(func(k, _ []byte) error {
			var grant Grant
			if err := get(tx, "grants", string(k), &grant); err != nil {
				return err
			}
			if grant.Request.Binding.NodeBoot == boot && !grant.Sealed && !grant.RatesReleased {
				pending = append(pending, grant)
			}
			return nil
		}); err != nil {
			return err
		}
		if len(pending) == 0 {
			return nil
		}
		start, known := j.retiredAt[boot]
		if !known {
			start = j.openedAt
		}
		elapsed := time.Since(start)
		if j.retirementElapsed != nil {
			elapsed = j.retirementElapsed(start)
		}
		if start.IsZero() || elapsed < MaxLeaseDuration {
			return ErrCapacity
		}
		for _, grant := range pending {
			var account Account
			if err := get(tx, "accounts", grant.Request.Binding.ClientID, &account); err != nil {
				return err
			}
			if err := releaseGrantRates(&account, grant.Request); err != nil {
				return err
			}
			if err := advanceRevision(&account); err != nil {
				return err
			}
			grant.RatesReleased = true
			if err := put(tx, "grants", grant.GrantID, grant); err != nil {
				return err
			}
			if err := put(tx, "accounts", account.Seed.ClientID, account); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReleaseRetiredClientRatesPage uses the existing per-node/client request index.
// Each transaction visits at most limit records and never credits billed quota.
// Empty next means the indexed range ended; repeating a page is idempotent.
func (j *Journal) ReleaseRetiredClientRatesPage(nodeID, clientID, after string, limit int) (next string, err error) {
	if j == nil || j.closed.Load() {
		return "", ErrJournal
	}
	if !key(nodeID) || !key(clientID) || after != "" && !key(after) || limit < 1 || limit > 1000 {
		return "", ErrRequest
	}
	j.retirementMu.Lock()
	defer j.retirementMu.Unlock()
	err = j.update(func(tx *bolt.Tx) error {
		var current NodeBoot
		if err := get(tx, "nodes", nodeID, &current); err != nil {
			return err
		}
		prefix := strings.TrimSuffix(compound(nodeID, clientID), "]") + ","
		cursor := tx.Bucket([]byte("requests")).Cursor()
		start := compound(nodeID, clientID, after)
		k, v := cursor.Seek([]byte(start))
		if string(k) == start {
			k, v = cursor.Next()
		}
		for visited := 0; k != nil && strings.HasPrefix(string(k), prefix) && visited < limit; visited++ {
			var grant Grant
			if err := get(tx, "grants", string(v), &grant); err != nil {
				return err
			}
			binding := grant.Request.Binding
			if binding.Identity != j.id || binding.ClientID != clientID || binding.NodeBoot.NodeID != nodeID || binding.NodeBoot.SourceID != current.SourceID || string(k) != compound(nodeID, clientID, grant.Request.RequestID) {
				return ErrJournal
			}
			next = grant.Request.RequestID
			if !grant.Sealed && !grant.RatesReleased && binding.NodeBoot != current {
				var recorded NodeBoot
				if err := get(tx, "boots", compound(nodeID, binding.NodeBoot.BootID), &recorded); err != nil || recorded != binding.NodeBoot {
					return ErrIncarnation
				}
				start, known := j.retiredAt[binding.NodeBoot]
				if !known {
					start = j.openedAt
				}
				elapsed := time.Since(start)
				if j.retirementElapsed != nil {
					elapsed = j.retirementElapsed(start)
				}
				if !start.IsZero() && elapsed >= MaxLeaseDuration {
					var account Account
					if err := get(tx, "accounts", clientID, &account); err != nil {
						return err
					}
					if err := releaseGrantRates(&account, grant.Request); err != nil {
						return err
					}
					if err := advanceRevision(&account); err != nil {
						return err
					}
					grant.RatesReleased = true
					if err := put(tx, "grants", grant.GrantID, grant); err != nil {
						return err
					}
					if err := put(tx, "accounts", clientID, account); err != nil {
						return err
					}
				}
			}
			k, v = cursor.Next()
		}
		if k == nil || !strings.HasPrefix(string(k), prefix) {
			next = ""
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return next, nil
}

func releaseGrantRates(account *Account, request Request) error {
	if account.UploadHeld.Rate < request.Upload.Rate || account.UploadHeld.Burst < request.Upload.Burst || account.DownloadHeld.Rate < request.Download.Rate || account.DownloadHeld.Burst < request.Download.Burst {
		return ErrJournal
	}
	account.UploadHeld.Rate -= request.Upload.Rate
	account.UploadHeld.Burst -= request.Upload.Burst
	account.DownloadHeld.Rate -= request.Download.Rate
	account.DownloadHeld.Burst -= request.Download.Burst
	if request.Upload.Unlimited {
		if account.UploadUnlimitedHeld == 0 {
			return ErrJournal
		}
		account.UploadUnlimitedHeld--
	}
	if request.Download.Unlimited {
		if account.DownloadUnlimitedHeld == 0 {
			return ErrJournal
		}
		account.DownloadUnlimitedHeld--
	}
	return nil
}
