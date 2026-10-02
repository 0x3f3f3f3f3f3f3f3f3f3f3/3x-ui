package policyauthority

import (
	"encoding/json"
	"errors"
	bolt "go.etcd.io/bbolt"
)

var ErrNotFound = errors.New("authority record not found")

func (j *Journal) AccountPage(after string, limit int) ([]Account, error) {
	if j == nil || j.closed.Load() || limit < 1 || limit > 1000 || after != "" && !key(after) {
		return nil, ErrRequest
	}
	var accounts []Account
	err := j.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket([]byte("accounts")).Cursor()
		k, v := cursor.Seek([]byte(after))
		if string(k) == after {
			k, v = cursor.Next()
		}
		for ; k != nil && len(accounts) < limit; k, v = cursor.Next() {
			var account Account
			if len(v) > maxRecordBytes || json.Unmarshal(v, &account) != nil || account.Seed.ClientID != string(k) {
				return ErrJournal
			}
			accounts = append(accounts, account)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return accounts, nil
}

func (j *Journal) Grant(id string) (Grant, error) {
	var grant Grant
	if j == nil || j.closed.Load() || !key(id) {
		return grant, ErrJournal
	}
	err := j.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("grants")).Get([]byte(id)) == nil {
			return ErrNotFound
		}
		return get(tx, "grants", id, &grant)
	})
	return grant, err
}

func (j *Journal) LookupRequest(nodeID, clientID, requestID string) (Grant, error) {
	var grant Grant
	if j == nil || j.closed.Load() || !key(nodeID) || !key(clientID) || !key(requestID) {
		return grant, ErrJournal
	}
	err := j.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte("requests")).Get([]byte(compound(nodeID, clientID, requestID)))
		if raw == nil {
			return ErrNotFound
		}
		return get(tx, "grants", string(raw), &grant)
	})
	return grant, err
}

func (j *Journal) CheckActiveGrant(id string, boot NodeBoot) error {
	if j == nil || j.closed.Load() || !key(id) || !validBoot(boot) {
		return ErrJournal
	}
	return j.db.View(func(tx *bolt.Tx) error {
		var grant Grant
		if err := get(tx, "grants", id, &grant); err != nil {
			return err
		}
		if grant.Request.Binding.NodeBoot != boot {
			return ErrIncarnation
		}
		if err := j.checkBinding(tx, grant.Request.Binding); err != nil {
			return err
		}
		var account Account
		if err := get(tx, "accounts", grant.Request.Binding.ClientID, &account); err != nil {
			return err
		}
		if account.Deleted {
			return ErrDeleted
		}
		if grant.Sealed || grant.Request.Binding.PolicyVersion != account.Policy.Version || grant.Request.Binding.WindowID != account.Policy.WindowID {
			return ErrRequest
		}
		return nil
	})
}
