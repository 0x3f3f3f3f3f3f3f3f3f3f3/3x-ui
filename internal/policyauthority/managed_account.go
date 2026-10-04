package policyauthority

import (
	"bytes"
	"encoding/json"
	"strings"

	bolt "go.etcd.io/bbolt"
)

const managedAccountBucket = "managed-account-origins"

// ManagedAccountOrigin is immutable evidence under the original coordinator
// anchor. SQL account projections cannot change a retained parent or scope.
type ManagedAccountOrigin struct {
	ClientID             string `json:"clientId"`
	ParentClientID       string `json:"parentClientId"`
	Scope                string `json:"scope"`
	NodeID               string `json:"nodeId,omitempty"`
	SourceID             string `json:"sourceId,omitempty"`
	InitialPolicyVersion uint64 `json:"initialPolicyVersion"`
	PolicyDigest         string `json:"policyDigest"`
}

func validManagedOrigin(o ManagedAccountOrigin) bool {
	if !canonicalClientUUID(o.ClientID) || !canonicalClientUUID(o.ParentClientID) || o.InitialPolicyVersion == 0 || !bounded(o.InitialPolicyVersion) || !validSnapshotDigest(o.PolicyDigest) || strings.ToLower(o.PolicyDigest) != o.PolicyDigest {
		return false
	}
	if o.Scope == "global" {
		return o.ClientID == o.ParentClientID && o.NodeID == "" && o.SourceID == ""
	}
	return o.Scope == "node" && o.ClientID != o.ParentClientID && key(o.NodeID) && key(o.SourceID)
}

func managedOriginIndices(o ManagedAccountOrigin) map[string]string {
	indices := map[string]string{"p/" + o.ParentClientID: o.Scope}
	if o.Scope == "node" {
		indices["n/"+compound(o.ParentClientID, o.NodeID)] = "a/" + o.ClientID
		indices["s/"+compound(o.ParentClientID, o.SourceID)] = "a/" + o.ClientID
	}
	return indices
}

func readManagedOrigin(tx *bolt.Tx, client string) (ManagedAccountOrigin, error) {
	var origin ManagedAccountOrigin
	if err := get(tx, managedAccountBucket, "a/"+client, &origin); err != nil {
		return origin, err
	}
	raw, err := json.Marshal(origin)
	if err != nil || !validManagedOrigin(origin) || origin.ClientID != client || !bytes.Equal(raw, tx.Bucket([]byte(managedAccountBucket)).Get([]byte("a/"+client))) {
		return origin, ErrJournal
	}
	return origin, nil
}

func (j *Journal) ManagedAccountOrigin(client string) (ManagedAccountOrigin, error) {
	var origin ManagedAccountOrigin
	if j == nil || j.closed.Load() {
		return origin, ErrJournal
	}
	if !canonicalClientUUID(client) {
		return origin, ErrRequest
	}
	err := j.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(managedAccountBucket))
		if b == nil || b.Get([]byte("a/"+client)) == nil {
			return ErrNotFound
		}
		var err error
		origin, err = readManagedOrigin(tx, client)
		return err
	})
	return origin, err
}

// LookupManagedNodeOrigin recovers the canonical UUID from original indices,
// rather than allowing a restored SQL row to choose another account identity.
func (j *Journal) LookupManagedNodeOrigin(parent, node, source string) (ManagedAccountOrigin, error) {
	var origin ManagedAccountOrigin
	if j == nil || j.closed.Load() {
		return origin, ErrJournal
	}
	if !canonicalClientUUID(parent) || !key(node) || !key(source) {
		return origin, ErrRequest
	}
	err := j.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(managedAccountBucket))
		if b == nil {
			return ErrNotFound
		}
		n := b.Get([]byte("n/" + compound(parent, node)))
		s := b.Get([]byte("s/" + compound(parent, source)))
		if n == nil && s == nil {
			return ErrNotFound
		}
		if n == nil || s == nil || !bytes.Equal(n, s) || !strings.HasPrefix(string(n), "a/") {
			return ErrIdentity
		}
		var err error
		origin, err = readManagedOrigin(tx, strings.TrimPrefix(string(n), "a/"))
		if err != nil {
			return err
		}
		if origin.Scope != "node" || origin.ParentClientID != parent || origin.NodeID != node || origin.SourceID != source {
			return ErrIdentity
		}
		return nil
	})
	if err != nil {
		return ManagedAccountOrigin{}, err
	}
	return origin, nil
}

func freshManagedSeed(seed Seed) bool {
	return validSeed(seed) && seed.Usage == (Usage{}) && seed.WindowUsed == 0 && seed.WindowRemainder == 0 && seed.FrozenBilled == 0
}

func managedMappingOrigin(tx *bolt.Tx, meta metadata, side ClientMappingSide, m ClientMapping) error {
	if meta.Schema != 8 {
		return nil
	}
	if side != ClientMappingCoordinator {
		return ErrIdentity
	}
	o, err := readManagedOrigin(tx, m.GlobalClientID)
	if err != nil {
		return err
	}
	if o.InitialPolicyVersion != m.GlobalPolicyVersion || o.PolicyDigest != m.PolicyDigest || o.Scope == "node" && (o.NodeID != m.NodeID || o.SourceID != m.SourceID) {
		return ErrIdentity
	}
	return nil
}

func managedIssuanceBinding(tx *bolt.Tx, binding Binding) error {
	var meta metadata
	if err := get(tx, "metadata", "state", &meta); err != nil {
		return err
	}
	if meta.Schema != 8 {
		return nil
	}
	o, err := readManagedOrigin(tx, binding.ClientID)
	if err != nil {
		return err
	}
	if binding.PolicyVersion != o.InitialPolicyVersion {
		return ErrRequest
	}
	b := tx.Bucket([]byte(clientMappingBucket))
	if b == nil {
		return ErrJournal
	}
	key := mappingReverseKey(ClientMappingCoordinator, ClientMapping{SourceID: binding.NodeBoot.SourceID, GlobalClientID: binding.ClientID})
	primary := b.Get([]byte(key))
	if primary == nil {
		return ErrNotFound
	}
	record, err := readClientMapping(tx, meta, string(primary))
	if err != nil {
		return err
	}
	m := record.Mapping
	if record.Side != ClientMappingCoordinator || m.NodeID != binding.NodeBoot.NodeID || m.SourceID != binding.NodeBoot.SourceID || m.GlobalClientID != binding.ClientID || m.GlobalPolicyVersion != binding.PolicyVersion {
		return ErrIdentity
	}
	return managedMappingOrigin(tx, meta, record.Side, m)
}

// AddManagedAccount commits the seed and its origin together. Enrollment of a
// consumed local account requires a separate sealed handoff, never a new seed.
func (j *Journal) AddManagedAccount(seed Seed, origin ManagedAccountOrigin) error {
	if !freshManagedSeed(seed) || !validManagedOrigin(origin) || seed.ClientID != origin.ClientID || seed.Policy.Version != origin.InitialPolicyVersion {
		return ErrRequest
	}
	return j.update(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		if meta.Schema != 8 || meta.Identity != j.id {
			return ErrIdentity
		}
		b, accounts := tx.Bucket([]byte(managedAccountBucket)), tx.Bucket([]byte("accounts"))
		if b == nil || accounts == nil {
			return ErrJournal
		}
		if accounts.Get([]byte(seed.ClientID)) != nil {
			var prior Account
			if err := get(tx, "accounts", seed.ClientID, &prior); err != nil {
				return err
			}
			retained, err := readManagedOrigin(tx, seed.ClientID)
			if err != nil {
				return err
			}
			if prior.Deleted {
				return ErrDeleted
			}
			if retained != origin || prior.Seed != seed {
				return ErrRequest
			}
			return nil
		}
		if accounts.Stats().KeyN >= maxRecords || b.Sequence() >= maxRecords {
			return ErrJournal
		}
		for index, value := range managedOriginIndices(origin) {
			if raw := b.Get([]byte(index)); raw != nil && string(raw) != value {
				return ErrIdentity
			}
		}
		if err := put(tx, "accounts", seed.ClientID, initialAccount(seed)); err != nil {
			return err
		}
		if err := put(tx, managedAccountBucket, "a/"+seed.ClientID, origin); err != nil {
			return err
		}
		for index, value := range managedOriginIndices(origin) {
			if err := b.Put([]byte(index), []byte(value)); err != nil {
				return err
			}
		}
		_, err := b.NextSequence()
		return err
	})
}

func managedCoordinatorSource(tx *bolt.Tx, meta metadata, source string) error {
	if !key(source) || meta.MigrationSource != source || !validSnapshotDigest(meta.SnapshotDigest) {
		return ErrIdentity
	}
	var marker, role MigrationRecord
	if err := get(tx, "migration", compound("sources", source), &marker); err != nil {
		return err
	}
	if marker.Kind != "sources" || marker.Key != source || string(marker.Value) != `{"role":"managed-coordinator","schema":1}` {
		return ErrIdentity
	}
	if err := get(tx, "migration", compound("execution-role", source), &role); err != nil {
		return err
	}
	var execution map[string]json.RawMessage
	if role.Kind != "execution-role" || role.Key != source || json.Unmarshal(role.Value, &execution) != nil || len(execution) != 1 || string(execution["mode"]) != `"local"` {
		return ErrIdentity
	}
	return nil
}

// ActivateManagedCoordinator fences older writers before any account or node
// enrollment. The original source snapshot must identify an empty coordinator.
func (j *Journal) ActivateManagedCoordinator(source string) error {
	return j.update(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		if meta.Identity != j.id {
			return ErrIdentity
		}
		if err := managedCoordinatorSource(tx, meta, source); err != nil {
			return err
		}
		if meta.Schema == 8 {
			return validateManagedAccounts(tx, meta)
		}
		if meta.Schema < 4 || meta.Schema > 6 || meta.MappingBaseSchema != 0 || tx.Bucket([]byte(clientMappingBucket)) != nil || tx.Bucket([]byte(managedAccountBucket)) != nil {
			return ErrJournal
		}
		for _, name := range []string{"accounts", "nodes", "boots", "grants", "requests", "changes"} {
			if tx.Bucket([]byte(name)) == nil || tx.Bucket([]byte(name)).Stats().KeyN != 0 {
				return ErrRequest
			}
		}
		if _, err := tx.CreateBucket([]byte(clientMappingBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucket([]byte(managedAccountBucket)); err != nil {
			return err
		}
		meta.MappingBaseSchema, meta.Schema = meta.Schema, 8
		return put(tx, "metadata", "state", meta)
	})
}

func validateManagedAccounts(tx *bolt.Tx, meta metadata) error {
	b := tx.Bucket([]byte(managedAccountBucket))
	if meta.Schema != 8 {
		if b != nil {
			return ErrJournal
		}
		return nil
	}
	if b == nil || b.Stats().KeyN > 4*maxRecords || b.Sequence() > maxRecords {
		return ErrJournal
	}
	if err := managedCoordinatorSource(tx, meta, meta.MigrationSource); err != nil {
		return err
	}
	expected := make(map[string]string)
	count := uint64(0)
	cursor := b.Cursor()
	for k, _ := cursor.Seek([]byte("a/")); k != nil && strings.HasPrefix(string(k), "a/"); k, _ = cursor.Next() {
		origin, err := readManagedOrigin(tx, strings.TrimPrefix(string(k), "a/"))
		if err != nil {
			return err
		}
		var account Account
		if err := get(tx, "accounts", origin.ClientID, &account); err != nil {
			return err
		}
		if !freshManagedSeed(account.Seed) || account.Seed.ClientID != origin.ClientID || account.Seed.Policy.Version != origin.InitialPolicyVersion {
			return ErrJournal
		}
		expected[string(k)] = ""
		for index, value := range managedOriginIndices(origin) {
			if prior, ok := expected[index]; ok && prior != value {
				return ErrJournal
			}
			expected[index] = value
		}
		count++
	}
	if count != b.Sequence() || int(count) != tx.Bucket([]byte("accounts")).Stats().KeyN || len(expected) != b.Stats().KeyN {
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
