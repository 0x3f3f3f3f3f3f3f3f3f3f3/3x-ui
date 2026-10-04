package policyauthority

import (
	"strings"

	bolt "go.etcd.io/bbolt"
)

type ManagedAccountSnapshot struct {
	Origin  ManagedAccountOrigin
	Account Account
}

// ManagedAccountPage reads parent membership and balances in one original
// journal transaction. Node cursors use the existing parent/node index.
func (j *Journal) ManagedAccountPage(parent, afterNode string, limit int) ([]ManagedAccountSnapshot, error) {
	if j == nil || j.closed.Load() {
		return nil, ErrJournal
	}
	if !canonicalClientUUID(parent) || afterNode != "" && !key(afterNode) || limit < 1 || limit > 128 {
		return nil, ErrRequest
	}
	page := make([]ManagedAccountSnapshot, 0, limit)
	err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		b := tx.Bucket([]byte(managedAccountBucket))
		if meta.Schema != 8 || meta.Identity != j.id || b == nil {
			return ErrIdentity
		}
		appendAccount := func(client string) error {
			origin, err := readManagedOrigin(tx, client)
			if err != nil {
				return err
			}
			if origin.ParentClientID != parent {
				return ErrIdentity
			}
			for index, value := range managedOriginIndices(origin) {
				if string(b.Get([]byte(index))) != value {
					return ErrIdentity
				}
			}
			var account Account
			if err := get(tx, "accounts", client, &account); err != nil {
				return err
			}
			if account.Seed.ClientID != origin.ClientID {
				return ErrIdentity
			}
			page = append(page, ManagedAccountSnapshot{Origin: origin, Account: account})
			return nil
		}
		switch string(b.Get([]byte("p/" + parent))) {
		case "":
			return nil
		case "global":
			if afterNode != "" {
				return ErrRequest
			}
			return appendAccount(parent)
		case "node":
			prefix := "n/" + compound(parent)
			prefix = prefix[:len(prefix)-1] + ","
			seek := prefix
			if afterNode != "" {
				seek = "n/" + compound(parent, afterNode)
			}
			cursor := b.Cursor()
			k, v := cursor.Seek([]byte(seek))
			if afterNode != "" && string(k) == seek {
				k, v = cursor.Next()
			}
			for ; k != nil && strings.HasPrefix(string(k), prefix) && len(page) < limit; k, v = cursor.Next() {
				if !strings.HasPrefix(string(v), "a/") {
					return ErrJournal
				}
				if err := appendAccount(strings.TrimPrefix(string(v), "a/")); err != nil {
					return err
				}
				if string(k) != "n/"+compound(parent, page[len(page)-1].Origin.NodeID) {
					return ErrIdentity
				}
			}
			return nil
		default:
			return ErrJournal
		}
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}
