package policyauthority

import bolt "go.etcd.io/bbolt"

func (j *Journal) ChangePolicy(request ChangeRequest) (Change, error) {
	if j == nil || request.Identity != j.id {
		return Change{}, ErrIdentity
	}
	if !key(request.ClientID) || !key(request.RequestID) || request.ExpectedVersion == 0 || !bounded(request.ExpectedVersion) || !validPolicy(request.Policy) || request.Policy.Version <= request.ExpectedVersion {
		return Change{}, ErrRequest
	}
	var changed Change
	err := j.update(func(tx *bolt.Tx) error {
		var account Account
		if err := get(tx, "accounts", request.ClientID, &account); err != nil {
			return err
		}
		if account.Deleted {
			return ErrDeleted
		}
		changes := tx.Bucket([]byte("changes"))
		requestKey := compound(request.ClientID, request.RequestID)
		if changes.Get([]byte(requestKey)) != nil {
			if err := get(tx, "changes", requestKey, &changed); err != nil {
				return err
			}
			if changed.Request != request {
				return ErrRequest
			}
			return nil
		}
		if changes.Stats().KeyN >= maxRecords {
			return ErrJournal
		}
		if account.Policy.Version != request.ExpectedVersion {
			return ErrRequest
		}
		if request.Reset {
			if request.Policy.WindowID == account.Seed.Policy.WindowID || request.Policy.WindowID == account.Policy.WindowID {
				return ErrRequest
			}
			if err := changes.ForEach(func(k, v []byte) error {
				var prior Change
				if err := get(tx, "changes", string(k), &prior); err != nil {
					return err
				}
				if prior.Request.ClientID == request.ClientID && prior.Request.Policy.WindowID == request.Policy.WindowID {
					return ErrRequest
				}
				return nil
			}); err != nil {
				return err
			}
		} else if request.Policy.WindowID != account.Policy.WindowID {
			return ErrRequest
		}
		changed = Change{Request: request, PreviousPolicy: account.Policy, UsageBoundary: account.Usage, WindowUsedBefore: account.WindowUsed, WindowRemainderBefore: account.WindowRemainder, FrozenBefore: account.FrozenBilled}
		if request.Reset {
			account.WindowBaseline, account.WindowBaseUsed, account.WindowUsed = account.Usage.BilledBytes, 0, 0
			account.WindowBaselineRemainder, account.WindowBaseRemainder, account.WindowRemainder = account.Usage.Remainder, 0, 0
			account.FrozenBilled = 0
		}
		account.Policy = request.Policy
		if err := advanceRevision(&account); err != nil {
			return err
		}
		if err := put(tx, "changes", requestKey, changed); err != nil {
			return err
		}
		return put(tx, "accounts", request.ClientID, account)
	})
	if err != nil {
		return Change{}, err
	}
	return changed, nil
}
