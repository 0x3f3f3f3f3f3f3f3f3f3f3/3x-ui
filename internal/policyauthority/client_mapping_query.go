package policyauthority

import bolt "go.etcd.io/bbolt"

// LookupClientMappingAccount reads the original reverse index directly; SQL
// projections and local UUID guesses cannot prove account enrollment.
func (j *Journal) LookupClientMappingAccount(side ClientMappingSide, source, client string) (ClientMapping, error) {
	var result ClientMapping
	if j == nil || j.closed.Load() {
		return result, ErrJournal
	}
	if !validMappingSide(side) || !key(source) || !canonicalClientUUID(client) {
		return result, ErrRequest
	}
	err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		if meta.Identity != j.id || meta.Schema < 7 || meta.Schema > 8 {
			return ErrIdentity
		}
		b := tx.Bucket([]byte(clientMappingBucket))
		if b == nil {
			return ErrJournal
		}
		reverse := mappingReverseKey(side, ClientMapping{SourceID: source, GlobalClientID: client})
		primary := b.Get([]byte(reverse))
		if primary == nil {
			return ErrNotFound
		}
		record, err := readClientMapping(tx, meta, string(primary))
		if err != nil {
			return err
		}
		if record.Side != side || record.Mapping.SourceID != source || record.Mapping.GlobalClientID != client || mappingReverseKey(side, record.Mapping) != reverse {
			return ErrIdentity
		}
		result = record.Mapping
		return nil
	})
	return result, err
}
