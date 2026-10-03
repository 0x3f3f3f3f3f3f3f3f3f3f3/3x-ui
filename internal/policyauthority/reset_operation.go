package policyauthority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

const resetOperationBucket = "reset-operations"
const maxResetOperationBytes = 64 << 20
const resetOperationChunkBytes = 8 << 10

type ResetOperationCapture struct {
	Identity    Identity `json:"identity"`
	SourceID    string   `json:"sourceId"`
	RequestID   string   `json:"requestId"`
	CalendarKey string   `json:"calendarKey"`
	Snapshot    string   `json:"snapshot"`
}

// ResetOperationSummary omits the potentially large original selection.
type ResetOperationSummary struct {
	RequestID     string `json:"requestId"`
	SourceID      string `json:"sourceId"`
	CalendarKey   string `json:"calendarKey"`
	SnapshotBytes uint64 `json:"snapshotBytes"`
	Digest        string `json:"digest"`
}

type resetOperationHeader struct {
	ResetOperationSummary
	Identity Identity `json:"identity"`
	Chunks   uint64   `json:"chunks"`
}

type resetOperationChunk struct {
	Data []byte `json:"data"`
}

func resetChunkKey(requestID string, index uint64) string {
	return fmt.Sprintf("c/%s/%08x", hex.EncodeToString([]byte(requestID)), index)
}

func resetDigest(value string) bool {
	return len(value) == 64 && value == strings.ToLower(value) && validSnapshotDigest(value)
}

func validResetHeader(h resetOperationHeader, meta metadata) bool {
	return h.Identity == meta.Identity && key(meta.MigrationSource) && h.SourceID == meta.MigrationSource && key(h.RequestID) &&
		(h.CalendarKey == "" || resetDigest(h.CalendarKey)) && resetDigest(h.Digest) && h.SnapshotBytes >= 2 && h.SnapshotBytes <= maxResetOperationBytes &&
		h.Chunks == (h.SnapshotBytes+resetOperationChunkBytes-1)/resetOperationChunkBytes
}

func (j *Journal) CaptureResetOperation(request ResetOperationCapture) error {
	if j == nil || j.closed.Load() {
		return ErrJournal
	}
	if request.Identity != j.id {
		return ErrIdentity
	}
	trimmed := strings.TrimSpace(request.Snapshot)
	if !key(request.RequestID) || request.CalendarKey != "" && !resetDigest(request.CalendarKey) || len(request.Snapshot) > maxResetOperationBytes ||
		len(trimmed) == 0 || trimmed[0] != '{' || !utf8.ValidString(request.Snapshot) || !json.Valid([]byte(request.Snapshot)) {
		return ErrRequest
	}
	digest := sha256.Sum256([]byte(request.Snapshot))
	h := resetOperationHeader{ResetOperationSummary: ResetOperationSummary{RequestID: request.RequestID, SourceID: request.SourceID, CalendarKey: request.CalendarKey, SnapshotBytes: uint64(len(request.Snapshot)), Digest: hex.EncodeToString(digest[:])}, Identity: request.Identity, Chunks: uint64((len(request.Snapshot) + resetOperationChunkBytes - 1) / resetOperationChunkBytes)}
	err := j.update(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		if meta.Identity != request.Identity || !key(meta.MigrationSource) || request.SourceID != meta.MigrationSource {
			return ErrIdentity
		}
		if meta.Schema < 4 || meta.Schema > 6 {
			return ErrJournal
		}
		b := tx.Bucket([]byte(resetOperationBucket))
		if meta.Schema == 4 && b != nil || meta.Schema >= 5 && b == nil {
			return ErrJournal
		}
		if b != nil {
			if b.Get([]byte("h/"+request.RequestID)) != nil {
				original, err := readResetOperation(tx, meta, request.RequestID)
				if err != nil {
					return err
				}
				if original != request {
					return ErrRequest
				}
				return nil
			}
			if request.CalendarKey != "" && b.Get([]byte("i/"+request.CalendarKey)) != nil {
				return ErrRequest
			}
		}
		needed := int(h.Chunks) + 1
		if request.CalendarKey != "" {
			needed++
		}
		if b != nil && b.Stats().KeyN+needed > maxRecords {
			return ErrJournal
		}
		if b == nil {
			var err error
			b, err = tx.CreateBucket([]byte(resetOperationBucket))
			if err != nil {
				return err
			}
		}
		if err := put(tx, resetOperationBucket, "h/"+request.RequestID, h); err != nil {
			return err
		}
		for index, start := uint64(0), 0; start < len(request.Snapshot); index, start = index+1, start+resetOperationChunkBytes {
			end := min(start+resetOperationChunkBytes, len(request.Snapshot))
			if err := put(tx, resetOperationBucket, resetChunkKey(request.RequestID, index), resetOperationChunk{Data: []byte(request.Snapshot[start:end])}); err != nil {
				return err
			}
		}
		if request.CalendarKey != "" {
			if err := b.Put([]byte("i/"+request.CalendarKey), []byte(request.RequestID)); err != nil {
				return err
			}
		}
		if meta.Schema == 4 {
			meta.Schema = 5
		}
		return put(tx, "metadata", "state", meta)
	})
	if err != nil && !errors.Is(err, ErrJournal) && !errors.Is(err, ErrIdentity) && !errors.Is(err, ErrRequest) {
		return fmt.Errorf("%w: capture: %w", ErrJournal, err)
	}
	return err
}

func resetHeader(tx *bolt.Tx, meta metadata, requestID string) (resetOperationHeader, error) {
	var h resetOperationHeader
	b := tx.Bucket([]byte(resetOperationBucket))
	if b == nil {
		if meta.Schema == 4 {
			return h, ErrNotFound
		}
		return h, ErrJournal
	}
	if b.Get([]byte("h/"+requestID)) == nil {
		return h, ErrNotFound
	}
	if err := get(tx, resetOperationBucket, "h/"+requestID, &h); err != nil || h.RequestID != requestID || !validResetHeader(h, meta) {
		return h, ErrJournal
	}
	if h.CalendarKey != "" && string(b.Get([]byte("i/"+h.CalendarKey))) != requestID {
		return h, ErrJournal
	}
	return h, nil
}

type resetChunkReader struct {
	tx       *bolt.Tx
	bucket   string
	header   resetOperationHeader
	index    uint64
	data     []byte
	utf8Tail []byte
}

func (r *resetChunkReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.data) == 0 {
		if r.index == r.header.Chunks {
			if len(r.utf8Tail) != 0 {
				return 0, ErrJournal
			}
			return 0, io.EOF
		}
		var chunk resetOperationChunk
		bucket := r.bucket
		if bucket == "" {
			bucket = resetOperationBucket
		}
		if err := get(r.tx, bucket, resetChunkKey(r.header.RequestID, r.index), &chunk); err != nil {
			return 0, ErrJournal
		}
		wanted := min(uint64(resetOperationChunkBytes), r.header.SnapshotBytes-r.index*resetOperationChunkBytes)
		if uint64(len(chunk.Data)) != wanted {
			return 0, ErrJournal
		}
		text := chunk.Data
		if len(r.utf8Tail) != 0 {
			text = append(r.utf8Tail, text...)
		}
		r.utf8Tail = nil
		if !utf8.Valid(text) {
			for len(text) > 0 {
				if !utf8.FullRune(text) {
					r.utf8Tail = append([]byte(nil), text...)
					break
				}
				runeValue, size := utf8.DecodeRune(text)
				if runeValue == utf8.RuneError && size == 1 {
					return 0, ErrJournal
				}
				text = text[size:]
			}
		}
		r.index++
		r.data = chunk.Data
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func readResetOperation(tx *bolt.Tx, meta metadata, requestID string) (ResetOperationCapture, error) {
	h, err := resetHeader(tx, meta, requestID)
	if err != nil {
		return ResetOperationCapture{}, err
	}
	value, err := readResetSnapshot(tx, resetOperationBucket, h)
	if err != nil {
		return ResetOperationCapture{}, err
	}
	return ResetOperationCapture{Identity: h.Identity, SourceID: h.SourceID, RequestID: h.RequestID, CalendarKey: h.CalendarKey, Snapshot: value}, nil
}

func readResetSnapshot(tx *bolt.Tx, bucket string, h resetOperationHeader) (string, error) {
	var snapshot strings.Builder
	snapshot.Grow(int(h.SnapshotBytes))
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(&snapshot, hash), &resetChunkReader{tx: tx, bucket: bucket, header: h}); err != nil || hex.EncodeToString(hash.Sum(nil)) != h.Digest {
		return "", ErrJournal
	}
	value := snapshot.String()
	if !utf8.ValidString(value) || !json.Valid([]byte(value)) || strings.TrimSpace(value)[0] != '{' {
		return "", ErrJournal
	}
	return value, nil
}

func (j *Journal) LookupResetOperation(requestID string) (ResetOperationCapture, error) {
	if j == nil || j.closed.Load() {
		return ResetOperationCapture{}, ErrJournal
	}
	if !key(requestID) {
		return ResetOperationCapture{}, ErrRequest
	}
	var result ResetOperationCapture
	err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		var err error
		result, err = readResetOperation(tx, meta, requestID)
		return err
	})
	return result, err
}

func (j *Journal) LookupResetCalendar(calendarKey string) (ResetOperationCapture, error) {
	if j == nil || j.closed.Load() {
		return ResetOperationCapture{}, ErrJournal
	}
	if !resetDigest(calendarKey) {
		return ResetOperationCapture{}, ErrRequest
	}
	var result ResetOperationCapture
	err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		b := tx.Bucket([]byte(resetOperationBucket))
		if b == nil {
			if meta.Schema == 4 {
				return ErrNotFound
			}
			return ErrJournal
		}
		requestID := string(b.Get([]byte("i/" + calendarKey)))
		if requestID == "" {
			return ErrNotFound
		}
		var err error
		result, err = readResetOperation(tx, meta, requestID)
		if err != nil || result.CalendarKey != calendarKey {
			return ErrJournal
		}
		return nil
	})
	return result, err
}

func (j *Journal) ResetOperationPage(after string, limit int) ([]ResetOperationSummary, error) {
	if j == nil || j.closed.Load() {
		return nil, ErrJournal
	}
	if after != "" && !key(after) || limit < 1 || limit > 128 {
		return nil, ErrRequest
	}
	var result []ResetOperationSummary
	err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		b := tx.Bucket([]byte(resetOperationBucket))
		if b == nil {
			if meta.Schema == 4 {
				return nil
			}
			return ErrJournal
		}
		cursor := b.Cursor()
		seek := "h/" + after
		k, _ := cursor.Seek([]byte(seek))
		if after != "" && string(k) == seek {
			k, _ = cursor.Next()
		}
		for ; k != nil && strings.HasPrefix(string(k), "h/") && len(result) < limit; k, _ = cursor.Next() {
			h, err := resetHeader(tx, meta, string(k)[2:])
			if err != nil {
				return ErrJournal
			}
			result = append(result, h.ResetOperationSummary)
		}
		return nil
	})
	return result, err
}

func (j *Journal) validateResetOperations(tx *bolt.Tx, meta metadata) error {
	b := tx.Bucket([]byte(resetOperationBucket))
	if meta.Schema == 4 {
		if b != nil {
			return ErrJournal
		}
		return nil
	}
	if b == nil || b.Stats().KeyN == 0 || b.Stats().KeyN > maxRecords || !key(meta.MigrationSource) {
		return ErrJournal
	}
	expectedRecords := 0
	err := b.ForEach(func(k, v []byte) error {
		if v == nil || len(v) > maxRecordBytes {
			return ErrJournal
		}
		if strings.HasPrefix(string(k), "c/") || strings.HasPrefix(string(k), "i/") {
			return nil
		}
		if !strings.HasPrefix(string(k), "h/") {
			return ErrJournal
		}
		h, err := resetHeader(tx, meta, string(k)[2:])
		if err != nil {
			return ErrJournal
		}
		expectedRecords += 1 + int(h.Chunks)
		if h.CalendarKey != "" {
			expectedRecords++
		}
		if expectedRecords > maxRecords {
			return ErrJournal
		}
		return validateResetSnapshot(tx, resetOperationBucket, h)
	})
	if err != nil || expectedRecords != b.Stats().KeyN {
		return ErrJournal
	}
	return nil
}

func validateResetSnapshot(tx *bolt.Tx, bucket string, h resetOperationHeader) error {
	hash := sha256.New()
	if _, err := io.Copy(hash, &resetChunkReader{tx: tx, bucket: bucket, header: h}); err != nil || hex.EncodeToString(hash.Sum(nil)) != h.Digest {
		return ErrJournal
	}
	decoder := json.NewDecoder(&resetChunkReader{tx: tx, bucket: bucket, header: h})
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return ErrJournal
	}
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return ErrJournal
		}
		if delim, ok := token.(json.Delim); ok {
			if delim == '{' || delim == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrJournal
	}
	return nil
}
