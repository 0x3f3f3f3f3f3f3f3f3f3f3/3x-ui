package policyauthority

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

func resetTestDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func TestResetOperationProgressRetainsExactDependenciesAcrossReopen(t *testing.T) {
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	original := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "original", CalendarKey: strings.Repeat("a", 64), Snapshot: `{"members":"` + strings.Repeat("原始,", 6000) + `"}`}
	if err := j.CaptureResetOperation(original); err != nil {
		t.Fatal(err)
	}
	prepared := ResetOperationPreparation{Identity: id, SourceID: original.SourceID, RequestID: original.RequestID, CaptureDigest: resetTestDigest(original.Snapshot), Snapshot: `{"effect":"` + strings.Repeat("已准备,", 6000) + `","number":1e400}`}
	if err := j.PrepareResetOperation(prepared); err != nil {
		t.Fatal(err)
	}
	completed := ResetOperationCompletion{Identity: id, SourceID: original.SourceID, RequestID: original.RequestID, PreparationDigest: resetTestDigest(prepared.Snapshot)}
	if err := j.CompleteResetOperation(completed); err != nil {
		t.Fatal(err)
	}
	later := ResetOperationCapture{Identity: id, SourceID: original.SourceID, RequestID: "z-later", Snapshot: `{"new":"selection"}`}
	if err := j.CaptureResetOperation(later); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatalf("exact protected reset dependencies did not reopen: %v", err)
	}
	defer j.Close()
	if err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		if meta.Schema != 6 {
			t.Fatalf("later capture downgraded the progress writer fence: %d", meta.Schema)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, capture := range []ResetOperationCapture{original, later} {
		got, err := j.LookupResetOperation(capture.RequestID)
		if err != nil || got != capture {
			t.Fatalf("original capture changed: %v", err)
		}
		if err := j.CaptureResetOperation(capture); err != nil {
			t.Fatal(err)
		}
	}
	got, err := j.LookupResetCalendar(original.CalendarKey)
	if err != nil || got != original {
		t.Fatalf("progress changed original calendar selection: %v", err)
	}
	p, err := j.LookupResetPreparation(prepared.RequestID)
	if err != nil || p != prepared {
		t.Fatalf("preparation changed across reopen: %v", err)
	}
	c, err := j.LookupResetCompletion(completed.RequestID)
	if err != nil || c != completed {
		t.Fatalf("completion changed across reopen: %v", err)
	}
	if err := j.PrepareResetOperation(prepared); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteResetOperation(completed); err != nil {
		t.Fatal(err)
	}
	page, err := j.ResetOperationPage("", 1)
	if err != nil || len(page) != 1 || page[0].RequestID != original.RequestID || page[0].Digest != prepared.CaptureDigest {
		t.Fatalf("progress changed bounded capture pages: %+v/%v", page, err)
	}
	page, err = j.ResetOperationPage(original.RequestID, 128)
	if err != nil || len(page) != 1 || page[0].RequestID != later.RequestID {
		t.Fatalf("capture page crossed original boundary: %+v/%v", page, err)
	}
	retained, err := j.Account("canonical-client")
	if err != nil || retained != account {
		t.Fatalf("reset witnesses changed funded account: %+v/%v", retained, err)
	}
	g, err := j.Grant(grant.GrantID)
	if err != nil || g != grant {
		t.Fatalf("reset witnesses changed issued grant: %+v/%v", g, err)
	}
}

func resetProgressRequests(id Identity) (ResetOperationCapture, ResetOperationPreparation, ResetOperationCompletion) {
	capture := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "original", Snapshot: `{"members":["canonical-client"]}`}
	prepared := ResetOperationPreparation{Identity: id, SourceID: capture.SourceID, RequestID: capture.RequestID, CaptureDigest: resetTestDigest(capture.Snapshot), Snapshot: `{"resets":[{"client":"canonical-client","version":2}]}`}
	completed := ResetOperationCompletion{Identity: id, SourceID: capture.SourceID, RequestID: capture.RequestID, PreparationDigest: resetTestDigest(prepared.Snapshot)}
	return capture, prepared, completed
}

func assertResetProgressFundedState(t *testing.T, j *Journal, account Account, grant Grant) {
	t.Helper()
	a, err := j.Account(account.Seed.ClientID)
	if err != nil || a != account {
		t.Fatalf("progress changed funded account: exact=%v err=%v", a == account, err)
	}
	g, err := j.Grant(grant.GrantID)
	if err != nil || g != grant {
		t.Fatalf("progress changed funded grant: exact=%v err=%v", g == grant, err)
	}
}

func TestResetOperationProgressRejectsConflictingDependencies(t *testing.T) {
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	capture, prepared, completed := resetProgressRequests(id)
	if err := j.PrepareResetOperation(prepared); !errors.Is(err, ErrNotFound) {
		t.Fatalf("preparation without capture: %v", err)
	}
	if err := j.CompleteResetOperation(completed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("completion without preparation: %v", err)
	}
	if err := j.CaptureResetOperation(capture); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteResetOperation(completed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("completion with only capture: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ResetOperationPreparation)
		want   error
	}{
		{"absent-capture", func(r *ResetOperationPreparation) { r.RequestID = "absent" }, ErrNotFound},
		{"source", func(r *ResetOperationPreparation) { r.SourceID = "different" }, ErrIdentity},
		{"empty-source", func(r *ResetOperationPreparation) { r.SourceID = "" }, ErrIdentity},
		{"identity", func(r *ResetOperationPreparation) { r.Identity.Generation++ }, ErrIdentity},
		{"request", func(r *ResetOperationPreparation) { r.RequestID = " " }, ErrRequest},
		{"invalid-digest", func(r *ResetOperationPreparation) { r.CaptureDigest = strings.Repeat("A", 64) }, ErrRequest},
		{"different-capture", func(r *ResetOperationPreparation) { r.CaptureDigest = strings.Repeat("b", 64) }, ErrRequest},
		{"json", func(r *ResetOperationPreparation) { r.Snapshot = `{"x":` }, ErrRequest},
		{"shape", func(r *ResetOperationPreparation) { r.Snapshot = `[]` }, ErrRequest},
		{"utf8", func(r *ResetOperationPreparation) { r.Snapshot = "{\"x\":\"\xff\"}" }, ErrRequest},
		{"oversize", func(r *ResetOperationPreparation) {
			r.Snapshot = `{"x":"` + strings.Repeat("a", maxResetOperationBytes) + `"}`
		}, ErrRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := prepared
			tc.mutate(&r)
			if err := j.PrepareResetOperation(r); !errors.Is(err, tc.want) {
				t.Fatalf("invalid preparation accepted: %v", err)
			}
		})
	}
	if _, err := j.LookupResetPreparation(prepared.RequestID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected preparation left witness: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("invalid preparation changed file bytes: %v", err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.PrepareResetOperation(prepared); err != nil {
		t.Fatal(err)
	}
	changed := prepared
	changed.Snapshot += " "
	if err := j.PrepareResetOperation(changed); !errors.Is(err, ErrRequest) {
		t.Fatalf("changed preparation replaced original bytes: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ResetOperationCompletion)
		want   error
	}{
		{"absent", func(r *ResetOperationCompletion) { r.RequestID = "absent" }, ErrNotFound},
		{"source", func(r *ResetOperationCompletion) { r.SourceID = "different" }, ErrIdentity},
		{"identity", func(r *ResetOperationCompletion) { r.Identity.Generation++ }, ErrIdentity},
		{"request", func(r *ResetOperationCompletion) { r.RequestID = " " }, ErrRequest},
		{"invalid-digest", func(r *ResetOperationCompletion) { r.PreparationDigest = "short" }, ErrRequest},
		{"different-preparation", func(r *ResetOperationCompletion) { r.PreparationDigest = strings.Repeat("b", 64) }, ErrRequest},
	} {
		t.Run("completion-"+tc.name, func(t *testing.T) {
			r := completed
			tc.mutate(&r)
			if err := j.CompleteResetOperation(r); !errors.Is(err, tc.want) {
				t.Fatalf("invalid completion accepted: %v", err)
			}
		})
	}
	if _, err := j.LookupResetCompletion(completed.RequestID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejection left completion: %v", err)
	}
	if err := j.CompleteResetOperation(completed); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteResetOperation(completed); err != nil {
		t.Fatal(err)
	}
	p, err := j.LookupResetPreparation(prepared.RequestID)
	if err != nil || p != prepared {
		t.Fatalf("conflicts changed preparation: %v", err)
	}
	c, err := j.LookupResetCompletion(completed.RequestID)
	if err != nil || c != completed {
		t.Fatalf("conflicts changed completion: %v", err)
	}
	for _, requestID := range []string{"", " ", "bad\x00key"} {
		if _, err := j.LookupResetPreparation(requestID); !errors.Is(err, ErrRequest) {
			t.Fatalf("invalid preparation lookup: %v", err)
		}
		if _, err := j.LookupResetCompletion(requestID); !errors.Is(err, ErrRequest) {
			t.Fatalf("invalid completion lookup: %v", err)
		}
	}
	assertResetProgressFundedState(t, j, account, grant)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareResetOperation(prepared); !errors.Is(err, ErrJournal) {
		t.Fatalf("closed preparation: %v", err)
	}
	if err := j.CompleteResetOperation(completed); !errors.Is(err, ErrJournal) {
		t.Fatalf("closed completion: %v", err)
	}
}

func TestResetOperationProgressCommitFailurePreservesPreviousState(t *testing.T) {
	for _, phase := range []string{"prepare", "complete"} {
		t.Run(phase, func(t *testing.T) {
			j, id, grant, path := resetCaptureFixture(t)
			account, err := j.Account("canonical-client")
			if err != nil {
				t.Fatal(err)
			}
			capture, prepared, completed := resetProgressRequests(id)
			if err := j.CaptureResetOperation(capture); err != nil {
				t.Fatal(err)
			}
			if phase == "complete" {
				if err := j.PrepareResetOperation(prepared); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			originalOpen := openJournal
			openJournal = func(path string, mode os.FileMode, options *bolt.Options) (*bolt.DB, error) {
				copyOptions := *options
				copyOptions.OpenFile = func(path string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(path) }
				return originalOpen(path, mode, &copyOptions)
			}
			readOnly, err := Open(path, id)
			openJournal = originalOpen
			if err != nil {
				t.Fatalf("read-only FD fixture did not reach commit: %v", err)
			}
			if phase == "prepare" {
				err = readOnly.PrepareResetOperation(prepared)
			} else {
				err = readOnly.CompleteResetOperation(completed)
			}
			if !errors.Is(err, ErrJournal) {
				t.Fatalf("read-only %s succeeded: %v", phase, err)
			}
			if err := readOnly.Close(); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("read-only %s changed file bytes: %v", phase, err)
			}
			j, err = Open(path, id)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			if phase == "prepare" {
				if _, err := j.LookupResetPreparation(prepared.RequestID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("failed preparation left witness: %v", err)
				}
				if err := j.PrepareResetOperation(prepared); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := j.LookupResetCompletion(completed.RequestID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("failed completion left witness: %v", err)
			}
			if err := j.CompleteResetOperation(completed); err != nil {
				t.Fatal(err)
			}
			p, err := j.LookupResetPreparation(prepared.RequestID)
			if err != nil || p != prepared {
				t.Fatalf("failed commit changed preparation: %v", err)
			}
			c, err := j.LookupResetCompletion(completed.RequestID)
			if err != nil || c != completed {
				t.Fatalf("completion retry changed original: %v", err)
			}
			assertResetProgressFundedState(t, j, account, grant)
		})
	}
}

func retainedResetProgressFixture(t *testing.T, path, phase string) string {
	t.Helper()
	root := os.Getenv("RESET_OPERATION_PROGRESS_FIXTURE_DIR")
	if root == "" {
		root = t.TempDir()
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("progress fixture directory is not private: %v", err)
	}
	dir, err := os.MkdirTemp(root, "reset-progress-")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(dir, phase+".db")
	if err := os.WriteFile(copyPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("retained closed progress fixture: %s", copyPath)
	return copyPath
}

func TestResetOperationProgressRejectsCorruption(t *testing.T) {
	j, id, _, path := resetCaptureFixture(t)
	capture, prepared, completed := resetProgressRequests(id)
	if err := j.CaptureResetOperation(capture); err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareResetOperation(prepared); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteResetOperation(completed); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	changeSchema := func(tx *bolt.Tx, schema uint64) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		meta.Schema = schema
		return put(tx, "metadata", "state", meta)
	}
	changeSnapshot := func(tx *bolt.Tx, snapshot string) error {
		var h resetPreparationHeader
		if err := get(tx, resetProgressBucket, "h/original", &h); err != nil {
			return err
		}
		h.Digest = resetTestDigest(snapshot)
		h.SnapshotBytes = uint64(len(snapshot))
		h.Chunks = 1
		if err := put(tx, resetProgressBucket, "h/original", h); err != nil {
			return err
		}
		if err := put(tx, resetProgressBucket, resetChunkKey("original", 0), resetOperationChunk{Data: []byte(snapshot)}); err != nil {
			return err
		}
		c := completed
		c.PreparationDigest = h.Digest
		return put(tx, resetProgressBucket, "d/original", c)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*bolt.Tx) error
	}{
		{"missing-chunk", func(tx *bolt.Tx) error {
			return tx.Bucket([]byte(resetProgressBucket)).Delete([]byte(resetChunkKey("original", 0)))
		}},
		{"altered-chunk", func(tx *bolt.Tx) error {
			return put(tx, resetProgressBucket, resetChunkKey("original", 0), resetOperationChunk{Data: []byte(`{"other":true}`)})
		}},
		{"orphan-chunk", func(tx *bolt.Tx) error {
			return put(tx, resetProgressBucket, resetChunkKey("orphan", 0), resetOperationChunk{Data: []byte(`{}`)})
		}},
		{"unknown-key", func(tx *bolt.Tx) error { return put(tx, resetProgressBucket, "unknown", map[string]string{}) }},
		{"missing-capture", func(tx *bolt.Tx) error { return tx.Bucket([]byte(resetOperationBucket)).Delete([]byte("h/original")) }},
		{"completion-without-preparation", func(tx *bolt.Tx) error { return tx.Bucket([]byte(resetProgressBucket)).Delete([]byte("h/original")) }},
		{"wrong-capture-digest", func(tx *bolt.Tx) error {
			var h resetPreparationHeader
			if err := get(tx, resetProgressBucket, "h/original", &h); err != nil {
				return err
			}
			h.CaptureDigest = strings.Repeat("b", 64)
			return put(tx, resetProgressBucket, "h/original", h)
		}},
		{"wrong-completion-digest", func(tx *bolt.Tx) error {
			c := completed
			c.PreparationDigest = strings.Repeat("b", 64)
			return put(tx, resetProgressBucket, "d/original", c)
		}},
		{"wrong-completion-source", func(tx *bolt.Tx) error {
			c := completed
			c.SourceID = "another"
			return put(tx, resetProgressBucket, "d/original", c)
		}},
		{"completion-for-orphan", func(tx *bolt.Tx) error {
			c := completed
			c.RequestID = "orphan"
			return put(tx, resetProgressBucket, "d/orphan", c)
		}},
		{"self-consistent-invalid-utf8", func(tx *bolt.Tx) error { return changeSnapshot(tx, "{\"x\":\"\xff\"}") }},
		{"self-consistent-invalid-json", func(tx *bolt.Tx) error { return changeSnapshot(tx, `{"x":}`) }},
		{"self-consistent-non-object", func(tx *bolt.Tx) error { return changeSnapshot(tx, `[]`) }},
		{"missing-progress-bucket", func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte(resetProgressBucket)) }},
		{"empty-progress-bucket", func(tx *bolt.Tx) error {
			if err := tx.DeleteBucket([]byte(resetProgressBucket)); err != nil {
				return err
			}
			_, err := tx.CreateBucket([]byte(resetProgressBucket))
			return err
		}},
		{"forbidden-schema5-progress", func(tx *bolt.Tx) error { return changeSchema(tx, 5) }},
		{"forbidden-schema4-progress", func(tx *bolt.Tx) error {
			if err := tx.DeleteBucket([]byte(resetOperationBucket)); err != nil {
				return err
			}
			return changeSchema(tx, 4)
		}},
		{"nested-progress-bucket", func(tx *bolt.Tx) error {
			_, err := tx.Bucket([]byte(resetProgressBucket)).CreateBucket([]byte("nested"))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupted := retainedResetProgressFixture(t, path, tc.name)
			db, err := bolt.Open(corrupted, 0600, journalOptions())
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(tc.mutate); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(corrupted)
			if err != nil {
				t.Fatal(err)
			}
			opened, err := Open(corrupted, id)
			if opened != nil {
				_ = opened.Close()
			}
			if !errors.Is(err, ErrJournal) {
				t.Fatalf("damaged reset progress was accepted: %v", err)
			}
			after, err := os.ReadFile(corrupted)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected progress corruption changed retained file bytes: %v", err)
			}
		})
	}
}

func TestResetOperationProgressLostReplyRetainsOriginalWitness(t *testing.T) {
	for _, phase := range []string{"prepare", "complete"} {
		t.Run(phase, func(t *testing.T) {
			j, id, grant, path := resetCaptureFixture(t)
			account, err := j.Account("canonical-client")
			if err != nil {
				t.Fatal(err)
			}
			capture, prepared, completed := resetProgressRequests(id)
			if err := j.CaptureResetOperation(capture); err != nil {
				t.Fatal(err)
			}
			if phase == "complete" {
				if err := j.PrepareResetOperation(prepared); err != nil {
					t.Fatal(err)
				}
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			child := exec.Command(os.Args[0], "-test.run=^TestResetOperationProgressReplyHelper$", "-test.timeout=30s")
			child.Env = append(os.Environ(), "RESET_OPERATION_PROGRESS_REPLY_PATH="+path, "RESET_OPERATION_PROGRESS_REPLY_PHASE="+phase)
			output, err := child.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 23 {
				t.Fatalf("helper did not exit after committed %s: %v/%s", phase, err, output)
			}
			retained := retainedResetProgressFixture(t, path, "lost-"+phase+"-reply")
			j, err = Open(retained, id)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			p, err := j.LookupResetPreparation(prepared.RequestID)
			if err != nil || p != prepared {
				t.Fatalf("lost %s reply erased original preparation: %v", phase, err)
			}
			c, err := j.LookupResetCompletion(completed.RequestID)
			if phase == "prepare" {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("preparation invented completion: %v", err)
				}
			} else if err != nil || c != completed {
				t.Fatalf("lost completion reply erased witness: %v", err)
			}
			if err := j.PrepareResetOperation(prepared); err != nil {
				t.Fatal(err)
			}
			if err := j.CompleteResetOperation(completed); err != nil {
				t.Fatal(err)
			}
			assertResetProgressFundedState(t, j, account, grant)
		})
	}
}

func TestResetOperationProgressReplyHelper(t *testing.T) {
	path := os.Getenv("RESET_OPERATION_PROGRESS_REPLY_PATH")
	if path == "" {
		return
	}
	id := Identity{AuthorityID: "reset-capture-authority", Generation: 1}
	j, err := Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	_, prepared, completed := resetProgressRequests(id)
	switch os.Getenv("RESET_OPERATION_PROGRESS_REPLY_PHASE") {
	case "prepare":
		err = j.PrepareResetOperation(prepared)
	case "complete":
		err = j.CompleteResetOperation(completed)
	default:
		t.Fatal("unknown reply-loss phase")
	}
	if err != nil {
		t.Fatal(err)
	}
	os.Exit(23)
}

func TestResetOperationProgressRecordExhaustionPreservesAllWitnesses(t *testing.T) {
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	capture, prepared, completed := resetProgressRequests(id)
	if err := j.CaptureResetOperation(capture); err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareResetOperation(prepared); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteResetOperation(completed); err != nil {
		t.Fatal(err)
	}
	for _, requestID := range []string{"pending-a", "pending-b", "unprepared"} {
		c := capture
		c.RequestID = requestID
		if err := j.CaptureResetOperation(c); err != nil {
			t.Fatal(err)
		}
		if requestID != "unprepared" {
			p := prepared
			p.RequestID = requestID
			if err := j.PrepareResetOperation(p); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Fill the real bound with complete dependency chains in one fixture transaction.
	// Every original/preparation/completion remains independently readable.
	err = j.db.Update(func(tx *bolt.Tx) error {
		var original resetOperationHeader
		var preparation resetPreparationHeader
		if err := get(tx, resetOperationBucket, "h/original", &original); err != nil {
			return err
		}
		if err := get(tx, resetProgressBucket, "h/original", &preparation); err != nil {
			return err
		}
		for i := 0; i < 33331; i++ {
			requestID := fmt.Sprintf("retained-%05d", i)
			h := original
			h.RequestID = requestID
			if err := put(tx, resetOperationBucket, "h/"+requestID, h); err != nil {
				return err
			}
			if err := put(tx, resetOperationBucket, resetChunkKey(requestID, 0), resetOperationChunk{Data: []byte(capture.Snapshot)}); err != nil {
				return err
			}
			p := preparation
			p.RequestID = requestID
			if err := put(tx, resetProgressBucket, "h/"+requestID, p); err != nil {
				return err
			}
			if err := put(tx, resetProgressBucket, resetChunkKey(requestID, 0), resetOperationChunk{Data: []byte(prepared.Snapshot)}); err != nil {
				return err
			}
			c := completed
			c.RequestID = requestID
			if err := put(tx, resetProgressBucket, "d/"+requestID, c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Stats counts committed pages; inspect after the fixture transaction spills.
	if err := j.db.View(func(tx *bolt.Tx) error {
		count := tx.Bucket([]byte(resetProgressBucket)).Stats().KeyN
		if count != 100000 {
			return fmt.Errorf("fixture missed actual progress record bound: %d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	overflow := prepared
	overflow.RequestID = "unprepared"
	if err := j.PrepareResetOperation(overflow); !errors.Is(err, ErrJournal) {
		t.Fatalf("progress record overflow accepted preparation: %v", err)
	}
	pending := completed
	pending.RequestID = "pending-a"
	if err := j.CompleteResetOperation(pending); !errors.Is(err, ErrJournal) {
		t.Fatalf("progress record overflow accepted completion: %v", err)
	}
	if err := j.PrepareResetOperation(prepared); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteResetOperation(completed); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.LookupResetPreparation("unprepared"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("overflow left preparation: %v", err)
	}
	if _, err := j.LookupResetCompletion("pending-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("overflow left completion: %v", err)
	}
	for _, requestID := range []string{"original", "retained-00000", "retained-33330"} {
		p, err := j.LookupResetPreparation(requestID)
		if err != nil || p.RequestID != requestID || p.Snapshot != prepared.Snapshot {
			t.Fatalf("bound changed retained preparation: %v", err)
		}
		c, err := j.LookupResetCompletion(requestID)
		if err != nil || c.RequestID != requestID || c.PreparationDigest != completed.PreparationDigest {
			t.Fatalf("bound changed retained completion: %v", err)
		}
	}
	assertResetProgressFundedState(t, j, account, grant)
}

func TestResetOperationProgressFileExhaustionPreservesAllWitnesses(t *testing.T) {
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	capture, prepared, completed := resetProgressRequests(id)
	if err := j.CaptureResetOperation(capture); err != nil {
		t.Fatal(err)
	}
	if err := j.PrepareResetOperation(prepared); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteResetOperation(completed); err != nil {
		t.Fatal(err)
	}
	candidate := capture
	candidate.RequestID = "overflow"
	if err := j.CaptureResetOperation(candidate); err != nil {
		t.Fatal(err)
	}
	err = j.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("test-file-pressure"))
		if err != nil {
			return err
		}
		return b.Put([]byte("retained-pressure"), make([]byte, 239<<20))
	})
	if err != nil {
		t.Fatal(err)
	}
	overflow := prepared
	overflow.RequestID = candidate.RequestID
	overflow.Snapshot = `{"effect":"` + strings.Repeat("a", 16<<20) + `"}`
	err = j.PrepareResetOperation(overflow)
	if !errors.Is(err, ErrJournal) || !errors.Is(err, berrors.ErrMaxSizeReached) {
		t.Fatalf("real file allocator overflow accepted preparation or lost cause: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.LookupResetPreparation(candidate.RequestID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("file exhaustion left partial preparation: %v", err)
	}
	got, err := j.LookupResetOperation(capture.RequestID)
	if err != nil || got != capture {
		t.Fatalf("file exhaustion changed capture: %v", err)
	}
	p, err := j.LookupResetPreparation(prepared.RequestID)
	if err != nil || p != prepared {
		t.Fatalf("file exhaustion changed original preparation: %v", err)
	}
	c, err := j.LookupResetCompletion(completed.RequestID)
	if err != nil || c != completed {
		t.Fatalf("file exhaustion changed original completion: %v", err)
	}
	assertResetProgressFundedState(t, j, account, grant)
}
