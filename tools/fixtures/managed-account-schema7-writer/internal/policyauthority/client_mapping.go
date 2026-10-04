package policyauthority

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

const clientMappingBucket = "client-mappings"

func stringIdentityGeneration(generation uint64) string { return strconv.FormatUint(generation, 10) }

func resetJournalSchema(meta metadata) uint64 {
	if meta.Schema == 7 {
		return meta.MappingBaseSchema
	}
	return meta.Schema
}

func mappingMetadata(tx *bolt.Tx, meta metadata) (*bolt.Bucket, error) {
	b := tx.Bucket([]byte(clientMappingBucket))
	if meta.Schema < 4 || meta.Schema > 7 {
		return nil, ErrJournal
	}
	if meta.Schema != 7 {
		if b != nil || meta.MappingBaseSchema != 0 {
			return nil, ErrJournal
		}
		return nil, nil
	}
	if meta.MappingBaseSchema < 4 || meta.MappingBaseSchema > 6 || b == nil || b.Sequence() == 0 || b.Sequence() > maxRecords {
		return nil, ErrJournal
	}
	return b, nil
}

type ClientMappingSide string

const (
	ClientMappingNode        ClientMappingSide = "node"
	ClientMappingCoordinator ClientMappingSide = "coordinator"
)

// ClientMapping is immutable enrollment evidence. A new boot does not change
// the source or original journal anchor; policy versions are local to each side.
type ClientMapping struct {
	Authority           Identity `json:"authority"`
	NodeAnchor          Identity `json:"nodeAnchor"`
	NodeID              string   `json:"nodeId"`
	SourceID            string   `json:"sourceId"`
	GlobalClientID      string   `json:"globalClientId"`
	LocalClientID       string   `json:"localClientId"`
	GlobalPolicyVersion uint64   `json:"globalPolicyVersion"`
	LocalPolicyVersion  uint64   `json:"localPolicyVersion"`
	PolicyDigest        string   `json:"policyDigest"`
}

type clientMappingRecord struct {
	Side    ClientMappingSide `json:"side"`
	Mapping ClientMapping     `json:"mapping"`
}

func canonicalClientUUID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

func validMappingSide(side ClientMappingSide) bool {
	return side == ClientMappingNode || side == ClientMappingCoordinator
}

func validClientMapping(m ClientMapping) bool {
	return key(m.Authority.AuthorityID) && m.Authority.Generation > 0 && key(m.NodeAnchor.AuthorityID) && m.NodeAnchor.Generation > 0 &&
		key(m.NodeID) && key(m.SourceID) && canonicalClientUUID(m.GlobalClientID) && canonicalClientUUID(m.LocalClientID) &&
		m.GlobalPolicyVersion > 0 && m.LocalPolicyVersion > 0 && bounded(m.Authority.Generation, m.NodeAnchor.Generation, m.GlobalPolicyVersion, m.LocalPolicyVersion) && validSnapshotDigest(m.PolicyDigest) && strings.ToLower(m.PolicyDigest) == m.PolicyDigest
}

func (m ClientMapping) Validate() error {
	if !validClientMapping(m) {
		return ErrRequest
	}
	return nil
}

// ClientMappingCursor is a canonical bounded cursor, never a client identity.
func ClientMappingCursor(m ClientMapping) string {
	return base64.RawURLEncoding.EncodeToString([]byte(m.SourceID + "\x00" + m.LocalClientID))
}

func mappingRecordKey(side ClientMappingSide, source, local string) string {
	return "m/" + compound(string(side), source, local)
}

func mappingReverseKey(side ClientMappingSide, m ClientMapping) string {
	return "r/" + compound(string(side), m.SourceID, m.GlobalClientID)
}

func mappingNodeKey(side ClientMappingSide, m ClientMapping) string {
	return "n/" + compound(string(side), m.NodeID)
}

func mappingAccount(tx *bolt.Tx, meta metadata, side ClientMappingSide, m ClientMapping, fresh bool) error {
	client, version := m.GlobalClientID, m.GlobalPolicyVersion
	if side == ClientMappingNode {
		if meta.Identity != m.NodeAnchor || meta.MigrationSource != m.SourceID {
			return ErrIdentity
		}
		var record MigrationRecord
		if err := get(tx, "migration", compound("execution-role", m.SourceID), &record); err != nil {
			return err
		}
		var role struct {
			Mode        string `json:"mode"`
			AuthorityID string `json:"authorityId"`
			Generation  uint64 `json:"generation"`
			NodeID      string `json:"nodeId"`
		}
		if json.Unmarshal(record.Value, &role) != nil || role.Mode != "delegated" || role.AuthorityID != m.Authority.AuthorityID || role.Generation != m.Authority.Generation || role.NodeID != m.NodeID {
			return ErrIdentity
		}
		client, version = m.LocalClientID, m.LocalPolicyVersion
	} else if meta.Identity != m.Authority {
		return ErrIdentity
	}
	var account Account
	if err := get(tx, "accounts", client, &account); err != nil {
		return err
	}
	if account.Policy.Version < version {
		return ErrRequest
	}
	if !fresh {
		return nil // Enrollment remains evidence after use, deletion or a later policy.
	}
	if account.Deleted {
		return ErrDeleted
	}
	if side == ClientMappingCoordinator {
		if account.Policy.Version != version {
			return ErrRequest
		}
		return nil // Adding a fresh node never rewrites the existing global balance.
	}
	if account.Revision != 1 || account.Policy.Version != version || account.Seed.Usage != (Usage{}) || account.Usage != (Usage{}) ||
		account.Seed.WindowUsed != 0 || account.Seed.WindowRemainder != 0 || account.Seed.FrozenBilled != 0 ||
		account.WindowUsed != 0 || account.WindowRemainder != 0 || account.FrozenBilled != 0 || account.HeldCapacity != 0 || account.HeldRemainder != 0 {
		return ErrRequest
	}
	return nil
}

func (j *Journal) RecordClientMapping(side ClientMappingSide, m ClientMapping) error {
	if j == nil || j.closed.Load() {
		return ErrJournal
	}
	if !validMappingSide(side) || !validClientMapping(m) {
		return ErrRequest
	}
	return j.update(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		if meta.Identity != j.id || meta.Schema < 4 || meta.Schema > 7 {
			return ErrJournal
		}
		b, err := mappingMetadata(tx, meta)
		if err != nil {
			return err
		}
		primary := mappingRecordKey(side, m.SourceID, m.LocalClientID)
		if b != nil {
			if raw := b.Get([]byte(primary)); raw != nil {
				previous, err := readClientMapping(tx, meta, primary)
				if err != nil {
					return err
				}
				if previous.Side != side || previous.Mapping != m {
					return ErrRequest
				}
				return mappingAccount(tx, meta, side, m, false)
			}
			if b.Get([]byte(mappingReverseKey(side, m))) != nil || b.Sequence() >= maxRecords {
				return ErrRequest
			}
			if raw := b.Get([]byte(mappingNodeKey(side, m))); raw != nil && string(raw) != compound(m.SourceID, m.NodeAnchor.AuthorityID, stringIdentityGeneration(m.NodeAnchor.Generation)) {
				return ErrIdentity
			}
		}
		if err := mappingAccount(tx, meta, side, m, true); err != nil {
			return err
		}
		if b == nil {
			var err error
			b, err = tx.CreateBucket([]byte(clientMappingBucket))
			if err != nil {
				return err
			}
			meta.MappingBaseSchema, meta.Schema = meta.Schema, 7
			if err := put(tx, "metadata", "state", meta); err != nil {
				return err
			}
		}
		if err := put(tx, clientMappingBucket, primary, clientMappingRecord{side, m}); err != nil {
			return err
		}
		if err := b.Put([]byte(mappingReverseKey(side, m)), []byte(primary)); err != nil {
			return err
		}
		if err := b.Put([]byte(mappingNodeKey(side, m)), []byte(compound(m.SourceID, m.NodeAnchor.AuthorityID, stringIdentityGeneration(m.NodeAnchor.Generation)))); err != nil {
			return err
		}
		_, err = b.NextSequence()
		return err
	})
}

func readClientMapping(tx *bolt.Tx, meta metadata, primary string) (clientMappingRecord, error) {
	var record clientMappingRecord
	if err := get(tx, clientMappingBucket, primary, &record); err != nil {
		return record, err
	}
	m := record.Mapping
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(canonical, tx.Bucket([]byte(clientMappingBucket)).Get([]byte(primary))) {
		return record, ErrJournal
	}
	if !validMappingSide(record.Side) || !validClientMapping(m) || primary != mappingRecordKey(record.Side, m.SourceID, m.LocalClientID) {
		return record, ErrJournal
	}
	b := tx.Bucket([]byte(clientMappingBucket))
	if string(b.Get([]byte(mappingReverseKey(record.Side, m)))) != primary || string(b.Get([]byte(mappingNodeKey(record.Side, m)))) != compound(m.SourceID, m.NodeAnchor.AuthorityID, stringIdentityGeneration(m.NodeAnchor.Generation)) {
		return record, ErrJournal
	}
	if err := mappingAccount(tx, meta, record.Side, m, false); err != nil {
		return record, err
	}
	return record, nil
}

func validateClientMappings(tx *bolt.Tx, meta metadata) error {
	b, err := mappingMetadata(tx, meta)
	if err != nil || b == nil {
		return err
	}
	if b.Stats().KeyN > 3*maxRecords {
		return ErrJournal
	}
	expected := make(map[string]string)
	count := uint64(0)
	cursor := b.Cursor()
	for k, _ := cursor.Seek([]byte("m/")); k != nil && strings.HasPrefix(string(k), "m/"); k, _ = cursor.Next() {
		record, err := readClientMapping(tx, meta, string(k))
		if err != nil {
			return err
		}
		m := record.Mapping
		expected[string(k)] = ""
		expected[mappingReverseKey(record.Side, m)] = string(k)
		expected[mappingNodeKey(record.Side, m)] = compound(m.SourceID, m.NodeAnchor.AuthorityID, stringIdentityGeneration(m.NodeAnchor.Generation))
		count++
	}
	if count != b.Sequence() || len(expected) != b.Stats().KeyN {
		return ErrJournal
	}
	return b.ForEach(func(k, v []byte) error {
		want, ok := expected[string(k)]
		if !ok || v == nil || want != "" && string(v) != want {
			return ErrJournal
		}
		return nil
	})
}

func (j *Journal) LookupClientMapping(side ClientMappingSide, sourceID, localClientID string) (ClientMapping, error) {
	var result ClientMapping
	if j == nil || j.closed.Load() {
		return result, ErrJournal
	}
	if !validMappingSide(side) || !key(sourceID) || !key(localClientID) {
		return result, ErrRequest
	}
	err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		b, err := mappingMetadata(tx, meta)
		if err != nil {
			return err
		}
		if b == nil {
			return ErrNotFound
		}
		primary := mappingRecordKey(side, sourceID, localClientID)
		if b.Get([]byte(primary)) == nil {
			return ErrNotFound
		}
		record, err := readClientMapping(tx, meta, primary)
		result = record.Mapping
		return err
	})
	if err != nil {
		return ClientMapping{}, err
	}
	return result, nil
}

func (j *Journal) ClientMappings(side ClientMappingSide, after string, limit int) ([]ClientMapping, error) {
	if j == nil || j.closed.Load() {
		return nil, ErrJournal
	}
	if !validMappingSide(side) || limit < 1 || limit > 128 || len(after) > 256 {
		return nil, ErrRequest
	}
	var afterKey string
	if after != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(after)
		parts := strings.Split(string(decoded), "\x00")
		if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != after || len(parts) != 2 || !key(parts[0]) || !canonicalClientUUID(parts[1]) {
			return nil, ErrRequest
		}
		afterKey = mappingRecordKey(side, parts[0], parts[1])
	}
	result := make([]ClientMapping, 0, limit)
	err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		b, err := mappingMetadata(tx, meta)
		if err != nil || b == nil {
			return err
		}
		prefix := "m/" + strings.TrimSuffix(compound(string(side)), "]") + ","
		cursor := b.Cursor()
		seek := prefix
		if after != "" {
			seek = afterKey
		}
		k, _ := cursor.Seek([]byte(seek))
		if after != "" && string(k) == seek {
			k, _ = cursor.Next()
		}
		for ; k != nil && strings.HasPrefix(string(k), prefix) && len(result) < limit; k, _ = cursor.Next() {
			record, err := readClientMapping(tx, meta, string(k))
			if err != nil {
				return err
			}
			result = append(result, record.Mapping)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
