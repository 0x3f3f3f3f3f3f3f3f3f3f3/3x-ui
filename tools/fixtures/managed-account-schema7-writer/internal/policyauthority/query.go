package policyauthority

import (
	"encoding/json"
	"errors"
	bolt "go.etcd.io/bbolt"
	"strings"
)

var ErrNotFound = errors.New("authority record not found")

func (j *Journal) ChangePage(clientID, after string, limit int) ([]Change, error) {
	if j == nil || j.closed.Load() || !key(clientID) || after != "" && !key(after) || limit < 1 || limit > 1000 {
		return nil, ErrRequest
	}
	var changes []Change
	prefix := compound(clientID)
	prefix = prefix[:len(prefix)-1] + ","
	err := j.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket([]byte("changes")).Cursor()
		seek := prefix
		if after != "" {
			seek = compound(clientID, after)
		}
		k, v := cursor.Seek([]byte(seek))
		if after != "" && string(k) == seek {
			k, v = cursor.Next()
		}
		for ; k != nil && strings.HasPrefix(string(k), prefix) && len(changes) < limit; k, v = cursor.Next() {
			var change Change
			if len(v) > maxRecordBytes || json.Unmarshal(v, &change) != nil || change.Request.Identity != j.id || change.Request.ClientID != clientID || !key(change.Request.RequestID) || string(k) != compound(clientID, change.Request.RequestID) || !validChangeEvidence(change.Request.Evidence) || !validResetBaseline(change.Request) {
				return ErrJournal
			}
			changes = append(changes, change)
		}
		return nil
	})
	return changes, err
}

// Migration rows can contain large original bulk membership. Bound both the
// number and encoded bytes of each page without scanning other kinds/clients.
func (j *Journal) MigrationPage(kind, keyPrefix, after string, limit int) ([]MigrationRecord, error) {
	if j == nil || j.closed.Load() || !validMigrationRecord(MigrationRecord{Kind: kind, Key: "probe", Value: json.RawMessage(`{}`)}) || len(keyPrefix) > 512 || after != "" && !strings.HasPrefix(after, keyPrefix) || limit < 1 || limit > 1000 {
		return nil, ErrRequest
	}
	var records []MigrationRecord
	prefix := compound(kind, keyPrefix)
	prefix = prefix[:len(prefix)-2]
	err := j.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket([]byte("migration")).Cursor()
		seek := prefix
		if after != "" {
			seek = compound(kind, after)
		}
		k, v := cursor.Seek([]byte(seek))
		if after != "" && string(k) == seek {
			k, v = cursor.Next()
		}
		encodedBytes := 0
		for ; k != nil && strings.HasPrefix(string(k), prefix) && len(records) < limit; k, v = cursor.Next() {
			if len(v) > maxMigrationRecordBytes+4096 {
				return ErrJournal
			}
			if encodedBytes+len(v) > maxMigrationRecordBytes+4096 {
				break
			}
			var record MigrationRecord
			if json.Unmarshal(v, &record) != nil || !validMigrationRecord(record) || record.Kind != kind || !strings.HasPrefix(record.Key, keyPrefix) || string(k) != compound(kind, record.Key) {
				return ErrJournal
			}
			records = append(records, record)
			encodedBytes += len(v)
		}
		return nil
	})
	return records, err
}

// LookupAccount distinguishes an unprovisioned identity from corrupt state.
func (j *Journal) LookupAccount(id string) (Account, error) {
	var account Account
	if j == nil || j.closed.Load() || !key(id) {
		return account, ErrJournal
	}
	err := j.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("accounts")).Get([]byte(id)) == nil {
			return ErrNotFound
		}
		return get(tx, "accounts", id, &account)
	})
	return account, err
}

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

func (j *Journal) LookupChange(clientID, requestID string) (Change, error) {
	var change Change
	if j == nil || j.closed.Load() || !key(clientID) || !key(requestID) {
		return change, ErrJournal
	}
	err := j.db.View(func(tx *bolt.Tx) error {
		requestKey := compound(clientID, requestID)
		if tx.Bucket([]byte("changes")).Get([]byte(requestKey)) == nil {
			return ErrNotFound
		}
		if err := get(tx, "changes", requestKey, &change); err != nil {
			return err
		}
		if change.Request.ClientID != clientID || change.Request.RequestID != requestID || change.Request.Identity != j.id || !validChangeEvidence(change.Request.Evidence) || !validResetBaseline(change.Request) {
			return ErrJournal
		}
		return nil
	})
	return change, err
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
