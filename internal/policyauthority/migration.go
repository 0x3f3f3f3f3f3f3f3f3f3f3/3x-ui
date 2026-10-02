package policyauthority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

const maxMigrationRecordBytes = 8 << 20
const maxMigrationRecords = 1000000

type MigrationRecord struct {
	Kind  string          `json:"kind"`
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

func CreateWithMigration(path string, seeds []Seed, deleted []string, records []MigrationRecord) (*Journal, Identity, error) {
	if len(records) > maxMigrationRecords || len(deleted) > len(seeds) {
		return nil, Identity{}, ErrRequest
	}
	members := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		members[seed.ClientID] = true
	}
	terminal := make(map[string]bool, len(deleted))
	for _, id := range deleted {
		if !members[id] || terminal[id] {
			return nil, Identity{}, ErrRequest
		}
		terminal[id] = true
	}
	ordered := append([]MigrationRecord(nil), records...)
	sort.Slice(ordered, func(i, j int) bool {
		return compound(ordered[i].Kind, ordered[i].Key) < compound(ordered[j].Kind, ordered[j].Key)
	})
	hash := sha256.New()
	var previous string
	var encodedBytes int
	for _, record := range ordered {
		if !validMigrationRecord(record) {
			return nil, Identity{}, ErrRequest
		}
		key := compound(record.Kind, record.Key)
		if key == previous {
			return nil, Identity{}, ErrRequest
		}
		previous = key
		raw, err := json.Marshal(record)
		if err != nil {
			return nil, Identity{}, ErrRequest
		}
		encodedBytes += len(key) + len(raw)
		if encodedBytes > maxJournalBytes/2 {
			return nil, Identity{}, ErrRequest
		}
		hash.Write([]byte(key))
		hash.Write([]byte{0})
		hash.Write(raw)
		hash.Write([]byte{0})
	}
	return createJournal(path, seeds, terminal, ordered, hex.EncodeToString(hash.Sum(nil)))
}

func (j *Journal) MigrationRecord(kind, key string) (MigrationRecord, error) {
	var record MigrationRecord
	if j == nil || j.closed.Load() {
		return record, ErrJournal
	}
	err := j.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket([]byte("migration")).Get([]byte(compound(kind, key)))
		if raw == nil {
			return ErrNotFound
		}
		if len(raw) > maxMigrationRecordBytes+4096 || json.Unmarshal(raw, &record) != nil || !validMigrationRecord(record) {
			return ErrJournal
		}
		return nil
	})
	return record, err
}

func validMigrationRecord(record MigrationRecord) bool {
	switch record.Kind {
	case "sources", "totals", "receipts", "resets", "reset-times", "reset-batches", "tombstones", "client-policy", "orphan-execution", "legacy-traffic", "execution-state", "execution-clients":
	default:
		return false
	}
	return record.Key != "" && len(record.Key) <= 512 && utf8.ValidString(record.Key) && strings.TrimSpace(record.Key) == record.Key && !strings.ContainsAny(record.Key, "\x00\r\n") && len(record.Value) > 0 && len(record.Value) <= maxMigrationRecordBytes && json.Valid(record.Value)
}

func putMigrationRecord(tx *bolt.Tx, record MigrationRecord) error {
	if !validMigrationRecord(record) {
		return ErrRequest
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return tx.Bucket([]byte("migration")).Put([]byte(compound(record.Kind, record.Key)), raw)
}

func validateMigrationRecords(tx *bolt.Tx, expected string) error {
	if len(expected) != 64 {
		return ErrJournal
	}
	hash := sha256.New()
	var encodedBytes int
	err := tx.Bucket([]byte("migration")).ForEach(func(k, v []byte) error {
		var record MigrationRecord
		if len(v) > maxMigrationRecordBytes+4096 || json.Unmarshal(v, &record) != nil || !validMigrationRecord(record) || string(k) != compound(record.Kind, record.Key) {
			return ErrJournal
		}
		encodedBytes += len(k) + len(v)
		if encodedBytes > maxJournalBytes/2 {
			return ErrJournal
		}
		hash.Write(k)
		hash.Write([]byte{0})
		hash.Write(v)
		hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return ErrJournal
	}
	return nil
}
