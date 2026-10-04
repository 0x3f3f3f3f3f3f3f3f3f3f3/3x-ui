package policyauthority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

const resetProgressBucket = "reset-operation-progress"

type ResetOperationPreparation struct {
	Identity      Identity `json:"identity"`
	SourceID      string   `json:"sourceId"`
	RequestID     string   `json:"requestId"`
	CaptureDigest string   `json:"captureDigest"`
	Snapshot      string   `json:"snapshot"`
}

type ResetOperationCompletion struct {
	Identity          Identity `json:"identity"`
	SourceID          string   `json:"sourceId"`
	RequestID         string   `json:"requestId"`
	PreparationDigest string   `json:"preparationDigest"`
}

type resetPreparationHeader struct {
	resetOperationHeader
	CaptureDigest string `json:"captureDigest"`
}

func progressWriteError(action string, err error) error {
	if err != nil && !errors.Is(err, ErrJournal) && !errors.Is(err, ErrIdentity) && !errors.Is(err, ErrRequest) && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: %s: %w", ErrJournal, action, err)
	}
	return err
}

func resetProgressMetadata(tx *bolt.Tx, id Identity, source string) (metadata, error) {
	var meta metadata
	if err := get(tx, "metadata", "state", &meta); err != nil {
		return meta, err
	}
	if meta.Identity != id || !key(meta.MigrationSource) || meta.MigrationSource != source {
		return meta, ErrIdentity
	}
	if resetJournalSchema(meta) < 4 || resetJournalSchema(meta) > 6 {
		return meta, ErrJournal
	}
	_, err := resetProgressRecords(tx, meta)
	return meta, err
}

func resetProgressRecords(tx *bolt.Tx, meta metadata) (*bolt.Bucket, error) {
	b := tx.Bucket([]byte(resetProgressBucket))
	if resetJournalSchema(meta) == 4 || resetJournalSchema(meta) == 5 {
		if b != nil {
			return nil, ErrJournal
		}
		return nil, nil
	}
	if resetJournalSchema(meta) != 6 || b == nil {
		return nil, ErrJournal
	}
	return b, nil
}

func (j *Journal) PrepareResetOperation(request ResetOperationPreparation) error {
	if j == nil || j.closed.Load() {
		return ErrJournal
	}
	if request.Identity != j.id {
		return ErrIdentity
	}
	trimmed := strings.TrimSpace(request.Snapshot)
	if !key(request.RequestID) || !resetDigest(request.CaptureDigest) || len(request.Snapshot) > maxResetOperationBytes || len(trimmed) == 0 || trimmed[0] != '{' || !utf8.ValidString(request.Snapshot) || !json.Valid([]byte(request.Snapshot)) {
		return ErrRequest
	}
	digest := sha256.Sum256([]byte(request.Snapshot))
	h := resetPreparationHeader{resetOperationHeader: resetOperationHeader{ResetOperationSummary: ResetOperationSummary{RequestID: request.RequestID, SourceID: request.SourceID, Digest: hex.EncodeToString(digest[:]), SnapshotBytes: uint64(len(request.Snapshot))}, Identity: request.Identity, Chunks: uint64((len(request.Snapshot) + resetOperationChunkBytes - 1) / resetOperationChunkBytes)}, CaptureDigest: request.CaptureDigest}
	err := j.update(func(tx *bolt.Tx) error {
		meta, err := resetProgressMetadata(tx, request.Identity, request.SourceID)
		if err != nil {
			return err
		}
		original, err := resetHeader(tx, meta, request.RequestID)
		if err != nil {
			return err
		}
		if original.Digest != request.CaptureDigest {
			return ErrRequest
		}
		b := tx.Bucket([]byte(resetProgressBucket))
		if b != nil && b.Get([]byte("h/"+request.RequestID)) != nil {
			previous, err := readResetPreparation(tx, meta, request.RequestID)
			if err != nil {
				return err
			}
			if previous != request {
				return ErrRequest
			}
			return nil
		}
		if b != nil && b.Stats().KeyN+1+int(h.Chunks) > maxRecords {
			return ErrJournal
		}
		if b == nil {
			b, err = tx.CreateBucket([]byte(resetProgressBucket))
			if err != nil {
				return err
			}
		}
		if err := put(tx, resetProgressBucket, "h/"+request.RequestID, h); err != nil {
			return err
		}
		for index, start := uint64(0), 0; start < len(request.Snapshot); index, start = index+1, start+resetOperationChunkBytes {
			end := min(start+resetOperationChunkBytes, len(request.Snapshot))
			if err := put(tx, resetProgressBucket, resetChunkKey(request.RequestID, index), resetOperationChunk{Data: []byte(request.Snapshot[start:end])}); err != nil {
				return err
			}
		}
		if meta.Schema >= 7 {
			meta.MappingBaseSchema = 6
		} else {
			meta.Schema = 6
		}
		return put(tx, "metadata", "state", meta)
	})
	return progressWriteError("prepare reset", err)
}

func (j *Journal) CompleteResetOperation(request ResetOperationCompletion) error {
	if j == nil || j.closed.Load() {
		return ErrJournal
	}
	if request.Identity != j.id {
		return ErrIdentity
	}
	if !key(request.RequestID) || !resetDigest(request.PreparationDigest) {
		return ErrRequest
	}
	err := j.update(func(tx *bolt.Tx) error {
		meta, err := resetProgressMetadata(tx, request.Identity, request.SourceID)
		if err != nil {
			return err
		}
		h, err := resetPreparedHeader(tx, meta, request.RequestID)
		if err != nil {
			return err
		}
		if h.Digest != request.PreparationDigest {
			return ErrRequest
		}
		b := tx.Bucket([]byte(resetProgressBucket))
		if b.Get([]byte("d/"+request.RequestID)) != nil {
			previous, err := readResetCompletion(tx, meta, request.RequestID)
			if err != nil {
				return err
			}
			if previous != request {
				return ErrRequest
			}
			return nil
		}
		if b.Stats().KeyN >= maxRecords {
			return ErrJournal
		}
		return put(tx, resetProgressBucket, "d/"+request.RequestID, request)
	})
	return progressWriteError("complete reset", err)
}

func resetPreparedHeader(tx *bolt.Tx, meta metadata, requestID string) (resetPreparationHeader, error) {
	var h resetPreparationHeader
	b, err := resetProgressRecords(tx, meta)
	if err != nil {
		return h, err
	}
	if b == nil || b.Get([]byte("h/"+requestID)) == nil {
		return h, ErrNotFound
	}
	if err := get(tx, resetProgressBucket, "h/"+requestID, &h); err != nil || h.RequestID != requestID || h.CalendarKey != "" || !validResetHeader(h.resetOperationHeader, meta) || !resetDigest(h.CaptureDigest) {
		return h, ErrJournal
	}
	original, err := resetHeader(tx, meta, requestID)
	if err != nil || original.Digest != h.CaptureDigest {
		return h, ErrJournal
	}
	return h, nil
}

func readResetPreparation(tx *bolt.Tx, meta metadata, requestID string) (ResetOperationPreparation, error) {
	h, err := resetPreparedHeader(tx, meta, requestID)
	if err != nil {
		return ResetOperationPreparation{}, err
	}
	snapshot, err := readResetSnapshot(tx, resetProgressBucket, h.resetOperationHeader)
	if err != nil {
		return ResetOperationPreparation{}, err
	}
	return ResetOperationPreparation{Identity: h.Identity, SourceID: h.SourceID, RequestID: h.RequestID, CaptureDigest: h.CaptureDigest, Snapshot: snapshot}, nil
}

func readResetCompletion(tx *bolt.Tx, meta metadata, requestID string) (ResetOperationCompletion, error) {
	var completed ResetOperationCompletion
	b, err := resetProgressRecords(tx, meta)
	if err != nil {
		return completed, err
	}
	if b == nil || b.Get([]byte("d/"+requestID)) == nil {
		return completed, ErrNotFound
	}
	if err := get(tx, resetProgressBucket, "d/"+requestID, &completed); err != nil || completed.Identity != meta.Identity || completed.SourceID != meta.MigrationSource || completed.RequestID != requestID || !resetDigest(completed.PreparationDigest) {
		return completed, ErrJournal
	}
	h, err := resetPreparedHeader(tx, meta, requestID)
	if err != nil || h.Digest != completed.PreparationDigest {
		return completed, ErrJournal
	}
	return completed, nil
}

func (j *Journal) LookupResetPreparation(requestID string) (ResetOperationPreparation, error) {
	if j == nil || j.closed.Load() {
		return ResetOperationPreparation{}, ErrJournal
	}
	if !key(requestID) {
		return ResetOperationPreparation{}, ErrRequest
	}
	var result ResetOperationPreparation
	err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		var err error
		result, err = readResetPreparation(tx, meta, requestID)
		return err
	})
	return result, err
}

func (j *Journal) LookupResetCompletion(requestID string) (ResetOperationCompletion, error) {
	if j == nil || j.closed.Load() {
		return ResetOperationCompletion{}, ErrJournal
	}
	if !key(requestID) {
		return ResetOperationCompletion{}, ErrRequest
	}
	var result ResetOperationCompletion
	err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		var err error
		result, err = readResetCompletion(tx, meta, requestID)
		return err
	})
	return result, err
}

func validateResetProgress(tx *bolt.Tx, meta metadata) error {
	b, err := resetProgressRecords(tx, meta)
	if err != nil {
		return err
	}
	if b == nil {
		return nil
	}
	count := b.Stats().KeyN
	if count == 0 || count > maxRecords || !key(meta.MigrationSource) {
		return ErrJournal
	}
	expectedRecords := 0
	err = b.ForEach(func(k, v []byte) error {
		if v == nil || len(v) > maxRecordBytes {
			return ErrJournal
		}
		switch {
		case strings.HasPrefix(string(k), "c/"):
			return nil
		case strings.HasPrefix(string(k), "h/"):
			h, err := resetPreparedHeader(tx, meta, string(k)[2:])
			if err != nil {
				return ErrJournal
			}
			expectedRecords += 1 + int(h.Chunks)
			if expectedRecords > maxRecords {
				return ErrJournal
			}
			return validateResetSnapshot(tx, resetProgressBucket, h.resetOperationHeader)
		case strings.HasPrefix(string(k), "d/"):
			if _, err := readResetCompletion(tx, meta, string(k)[2:]); err != nil {
				return ErrJournal
			}
			expectedRecords++
			if expectedRecords > maxRecords {
				return ErrJournal
			}
			return nil
		default:
			return ErrJournal
		}
	})
	if err != nil || expectedRecords != count {
		return ErrJournal
	}
	return nil
}
