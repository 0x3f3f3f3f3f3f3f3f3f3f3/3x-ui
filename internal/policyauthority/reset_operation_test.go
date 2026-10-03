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
	"strconv"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

func resetCaptureFixture(t *testing.T) (*Journal, Identity, Grant, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "capture")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.db")
	id := Identity{AuthorityID: "reset-capture-authority", Generation: 1}
	seed := Seed{ClientID: "canonical-client", Policy: Policy{WindowID: "window-1", Version: 1, QuotaBytes: 100, Upload: Direction{Rate: 1000, Burst: 100}, Download: Direction{Rate: 2000, Burst: 200}}, Usage: Usage{RawUpload: 5, RawDownload: 5, BilledBytes: 10}, WindowUsed: 10}
	j, err := CreateWithMigrationSourceIdentity(path, id, "reset-source", []Seed{seed}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	boot := NodeBoot{NodeID: "node-a", SourceID: "reset-source", BootID: "capture-boot"}
	if err := j.RegisterBoot(boot); err != nil {
		t.Fatal(err)
	}
	grant, err := j.Issue(issueRequest(id, boot, "funded-grant", 60))
	if err != nil {
		t.Fatal(err)
	}
	return j, id, grant, path
}

func retainedResetFixture(t *testing.T, path, phase string) string {
	t.Helper()
	root := os.Getenv("RESET_OPERATION_FIXTURE_DIR")
	if root == "" {
		var err error
		root, err = os.MkdirTemp(t.TempDir(), "private-reset-fixtures-")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("fixture directory is not private: %v", err)
	}
	dir, err := os.MkdirTemp(root, "reset-operation-")
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
	t.Logf("retained closed fixture: %s", copyPath)
	return copyPath
}

func originalResetWriter(t *testing.T, probe, path string, id Identity, wantOpen bool) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(probe, path, id.AuthorityID, strconv.FormatUint(id.Generation, 10)).CombinedOutput()
	if wantOpen {
		if err != nil || string(output) != "OPENED\n" {
			t.Fatalf("original writer did not open schema4: %v/%s", err, output)
		}
	} else {
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 3 || string(output) != "REJECTED_JOURNAL\n" {
			t.Fatalf("original writer did not reject schema5: %v/%s", err, output)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("original writer changed fixture bytes: %v", err)
	}
}

func TestResetOperationCaptureActualOldWriter(t *testing.T) {
	probe := os.Getenv("RESET_OPERATION_OLD_WRITER_PROBE")
	if probe == "" {
		t.Skip("set RESET_OPERATION_OLD_WRITER_PROBE to the retained pre-change original-source executable")
	}
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	r := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "original-request", CalendarKey: strings.Repeat("a", 64), Snapshot: `{"targets":"` + strings.Repeat("原始,", 6000) + `"}`}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	originalResetWriter(t, probe, retainedResetFixture(t, path, "uncaptured-schema4"), id, true)
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	invalid := r
	invalid.Snapshot = "{"
	if err := j.CaptureResetOperation(invalid); !errors.Is(err, ErrRequest) {
		t.Fatalf("invalid first capture accepted: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	originalResetWriter(t, probe, retainedResetFixture(t, path, "rejected-schema4"), id, true)
	originalOpen := openJournal
	openJournal = func(path string, mode os.FileMode, options *bolt.Options) (*bolt.DB, error) {
		copyOptions := *options
		copyOptions.OpenFile = func(path string, _ int, _ os.FileMode) (*os.File, error) { return os.Open(path) }
		return originalOpen(path, mode, &copyOptions)
	}
	readOnly, err := Open(path, id)
	openJournal = originalOpen
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnly.CaptureResetOperation(r); !errors.Is(err, ErrJournal) {
		t.Fatalf("read-only FD first capture committed: %v", err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	originalResetWriter(t, probe, retainedResetFixture(t, path, "write-failed-schema4"), id, true)
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.CaptureResetOperation(r); err != nil {
		_ = j.Close()
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	committed := retainedResetFixture(t, path, "committed-schema5")
	originalResetWriter(t, probe, committed, id, false)
	j, err = Open(committed, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	got, err := j.LookupResetCalendar(r.CalendarKey)
	if err != nil || got != r {
		t.Fatalf("new reader lost protected original: exact=%v err=%v", got == r, err)
	}
	if err := j.CaptureResetOperation(r); err != nil {
		t.Fatalf("new writer exact retry: %v", err)
	}
	after, err := j.Account("canonical-client")
	if err != nil || after != account {
		t.Fatalf("writer fence changed account: exact=%v err=%v", after == account, err)
	}
	retained, err := j.Grant(grant.GrantID)
	if err != nil || retained != grant {
		t.Fatalf("writer fence changed funded grant: exact=%v err=%v", retained == grant, err)
	}
}

func TestResetOperationCaptureCommitReplyHelper(t *testing.T) {
	path := os.Getenv("RESET_OPERATION_COMMIT_HELPER_PATH")
	if path == "" {
		return
	}
	id := Identity{AuthorityID: "reset-capture-authority", Generation: 1}
	j, err := Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	r := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "lost-reply", CalendarKey: strings.Repeat("d", 64), Snapshot: `{"targets":["original-client"]}`}
	if err := j.CaptureResetOperation(r); err != nil {
		t.Fatal(err)
	}
	// Exit before replying or closing the journal. The parent retains the file
	// and must determine the committed result by reopening, never recreation.
	os.Exit(23)
}

func TestResetOperationCaptureLostReplyRetainsCompleteWriterFence(t *testing.T) {
	probe := os.Getenv("RESET_OPERATION_OLD_WRITER_PROBE")
	if probe == "" {
		t.Skip("set RESET_OPERATION_OLD_WRITER_PROBE to verify the lost-reply fixture against its actual old writer")
	}
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	uncaptured := retainedResetFixture(t, path, "before-lost-reply-schema4")
	originalResetWriter(t, probe, uncaptured, id, true)
	child := exec.Command(os.Args[0], "-test.run=^TestResetOperationCaptureCommitReplyHelper$")
	child.Env = append(os.Environ(), "RESET_OPERATION_COMMIT_HELPER_PATH="+path)
	output, err := child.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 23 {
		t.Fatalf("capture did not reach lost-reply boundary: %v/%s", err, output)
	}
	committed := retainedResetFixture(t, path, "lost-reply-schema5")
	originalResetWriter(t, probe, committed, id, false)
	j, err = Open(committed, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	r := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "lost-reply", CalendarKey: strings.Repeat("d", 64), Snapshot: `{"targets":["original-client"]}`}
	got, err := j.LookupResetOperation(r.RequestID)
	if err != nil || got != r {
		t.Fatalf("lost reply left incomplete original capture: exact=%v err=%v", got == r, err)
	}
	if err := j.CaptureResetOperation(r); err != nil {
		t.Fatalf("lost-reply exact retry: %v", err)
	}
	after, err := j.Account("canonical-client")
	if err != nil || after != account {
		t.Fatalf("lost reply changed account: exact=%v err=%v", after == account, err)
	}
	retained, err := j.Grant(grant.GrantID)
	if err != nil || retained != grant {
		t.Fatalf("lost reply changed grant: exact=%v err=%v", retained == grant, err)
	}
}

func TestResetOperationCaptureFailureLeavesOldJournalUnchanged(t *testing.T) {
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	originalBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "original-request", Snapshot: `{}`}
	for _, invalid := range []struct {
		name   string
		mutate func(*ResetOperationCapture)
		want   error
	}{
		{"invalid-utf8", func(r *ResetOperationCapture) { r.Snapshot = "{\"x\":\"" + string([]byte{255}) + "\"}" }, ErrRequest},
		{"invalid-json", func(r *ResetOperationCapture) { r.Snapshot = "{" }, ErrRequest},
		{"array", func(r *ResetOperationCapture) { r.Snapshot = "[]" }, ErrRequest},
		{"null", func(r *ResetOperationCapture) { r.Snapshot = "null" }, ErrRequest},
		{"empty", func(r *ResetOperationCapture) { r.Snapshot = "" }, ErrRequest},
		{"blank-request", func(r *ResetOperationCapture) { r.RequestID = " " }, ErrRequest},
		{"bad-calendar", func(r *ResetOperationCapture) { r.CalendarKey = strings.Repeat("A", 64) }, ErrRequest},
		{"source", func(r *ResetOperationCapture) { r.SourceID = "" }, ErrIdentity},
		{"identity", func(r *ResetOperationCapture) { r.Identity.AuthorityID = "other" }, ErrIdentity},
		{"oversize", func(r *ResetOperationCapture) { r.Snapshot = `{"x":"` + strings.Repeat("a", 64<<20) + `"}` }, ErrRequest},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			changed := r
			invalid.mutate(&changed)
			if err := j.CaptureResetOperation(changed); !errors.Is(err, invalid.want) {
				t.Fatalf("invalid capture accepted: %v", err)
			}
		})
	}
	if _, err := j.LookupResetOperation(r.RequestID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejection left operation: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	afterBytes, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(afterBytes, originalBytes) {
		t.Fatalf("validation rejection changed journal bytes: %v", err)
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
		t.Fatalf("read-only FD fixture did not reach commit boundary: %v", err)
	}
	if err := readOnly.CaptureResetOperation(r); !errors.Is(err, ErrJournal) {
		t.Fatalf("read-only FD commit succeeded: %v", err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	afterBytes, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(afterBytes, originalBytes) {
		t.Fatalf("read-only rejection changed journal bytes: %v", err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err := j.db.View(func(tx *bolt.Tx) error {
		var meta metadata
		if err := get(tx, "metadata", "state", &meta); err != nil {
			return err
		}
		if meta.Schema != 4 || tx.Bucket([]byte("reset-operations")) != nil {
			return fmt.Errorf("partial storage transition")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := j.Account("canonical-client")
	if err != nil || after != account {
		t.Fatalf("failed capture changed account: exact=%v err=%v", after == account, err)
	}
	retained, err := j.Grant(grant.GrantID)
	if err != nil || retained != grant {
		t.Fatalf("failed capture changed grant: exact=%v err=%v", retained == grant, err)
	}
	if err := j.CaptureResetOperation(r); err != nil {
		t.Fatalf("retry after failed commit: %v", err)
	}
	got, err := j.LookupResetOperation(r.RequestID)
	if err != nil || got != r {
		t.Fatalf("retry did not retain original: exact=%v err=%v", got == r, err)
	}
}

func TestResetOperationCaptureRejectsCorruptChunks(t *testing.T) {
	for _, damage := range []struct {
		name   string
		mutate func(*bolt.Tx) error
	}{
		{"missing-chunk", func(tx *bolt.Tx) error {
			return tx.Bucket([]byte("reset-operations")).Delete([]byte("c/6f726967696e616c2d72657175657374/00000000"))
		}},
		{"altered-chunk", func(tx *bolt.Tx) error {
			return put(tx, "reset-operations", "c/6f726967696e616c2d72657175657374/00000000", resetOperationChunk{Data: []byte(strings.Repeat("x", 8192))})
		}},
		{"missing-calendar-index", func(tx *bolt.Tx) error {
			return tx.Bucket([]byte("reset-operations")).Delete([]byte("i/" + strings.Repeat("a", 64)))
		}},
		{"duplicate-calendar", func(tx *bolt.Tx) error {
			var h resetOperationHeader
			if err := get(tx, "reset-operations", "h/original-request", &h); err != nil {
				return err
			}
			h.RequestID = "second-request"
			return put(tx, "reset-operations", "h/second-request", h)
		}},
		{"orphan-chunk", func(tx *bolt.Tx) error {
			return put(tx, "reset-operations", "c/6f727068616e/00000000", resetOperationChunk{Data: []byte(`{}`)})
		}},
		{"unknown-entry", func(tx *bolt.Tx) error {
			return tx.Bucket([]byte("reset-operations")).Put([]byte("unknown"), []byte(`{}`))
		}},
		{"missing-bucket", func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("reset-operations")) }},
		{"old-schema-with-capture", func(tx *bolt.Tx) error {
			var meta metadata
			if err := get(tx, "metadata", "state", &meta); err != nil {
				return err
			}
			meta.Schema = 4
			return put(tx, "metadata", "state", meta)
		}},
		{"changed-source", func(tx *bolt.Tx) error {
			var h resetOperationHeader
			if err := get(tx, "reset-operations", "h/original-request", &h); err != nil {
				return err
			}
			h.SourceID = "other-source"
			return put(tx, "reset-operations", "h/original-request", h)
		}},
		{"self-consistent-invalid-utf8", func(tx *bolt.Tx) error {
			payload := []byte("{\"x\":\"" + string([]byte{255}) + "\"}")
			var h resetOperationHeader
			if err := get(tx, "reset-operations", "h/original-request", &h); err != nil {
				return err
			}
			b := tx.Bucket([]byte("reset-operations"))
			for i := uint64(0); i < h.Chunks; i++ {
				if err := b.Delete([]byte(resetChunkKey(h.RequestID, i))); err != nil {
					return err
				}
			}
			digest := sha256.Sum256(payload)
			h.SnapshotBytes = uint64(len(payload))
			h.Chunks = 1
			h.Digest = hex.EncodeToString(digest[:])
			if err := put(tx, "reset-operations", "h/original-request", h); err != nil {
				return err
			}
			return put(tx, "reset-operations", resetChunkKey(h.RequestID, 0), resetOperationChunk{Data: payload})
		}},
	} {
		t.Run(damage.name, func(t *testing.T) {
			j, id, _, path := resetCaptureFixture(t)
			r := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "original-request", CalendarKey: strings.Repeat("a", 64), Snapshot: `{"targets":"` + strings.Repeat("original,", 3000) + `"}`}
			if err := j.CaptureResetOperation(r); err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(path, 0600, journalOptions())
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(damage.mutate); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			opened, err := Open(path, id)
			if opened != nil {
				_ = opened.Close()
			}
			if !errors.Is(err, ErrJournal) {
				t.Fatalf("damaged capture opened: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected damaged journal was changed: %v", err)
			}
		})
	}
}

func TestResetOperationCaptureRetainsSelectionAcrossReopen(t *testing.T) {
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	calendar := strings.Repeat("a", 64)
	snapshot := `{"requestId":"original-request","targets":"` + strings.Repeat("原始-client,", 3000) + `"}`
	r := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "original-request", CalendarKey: calendar, Snapshot: snapshot}
	if err := j.CaptureResetOperation(r); err != nil {
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
	for _, lookup := range []func() (ResetOperationCapture, error){func() (ResetOperationCapture, error) { return j.LookupResetOperation(r.RequestID) }, func() (ResetOperationCapture, error) { return j.LookupResetCalendar(calendar) }} {
		got, err := lookup()
		if err != nil || got != r {
			t.Fatalf("original operation changed after reopen: exact=%v err=%v", got == r, err)
		}
	}
	if err := j.CaptureResetOperation(r); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	for _, change := range []struct {
		name   string
		mutate func(*ResetOperationCapture)
		want   error
	}{
		{"snapshot", func(r *ResetOperationCapture) { r.Snapshot = `{"targets":"new-client"}` }, ErrRequest},
		{"source", func(r *ResetOperationCapture) { r.SourceID = "another-source" }, ErrIdentity},
		{"identity", func(r *ResetOperationCapture) { r.Identity.Generation++ }, ErrIdentity},
		{"calendar", func(r *ResetOperationCapture) { r.CalendarKey = strings.Repeat("b", 64) }, ErrRequest},
		{"calendar-other-request", func(r *ResetOperationCapture) { r.RequestID = "replacement" }, ErrRequest},
	} {
		t.Run(change.name, func(t *testing.T) {
			changed := r
			change.mutate(&changed)
			if err := j.CaptureResetOperation(changed); !errors.Is(err, change.want) {
				t.Fatalf("conflicting capture: %v", err)
			}
		})
	}
	for _, requestID := range []string{"z-final", "a-first"} {
		manual := r
		manual.RequestID = requestID
		manual.CalendarKey = ""
		manual.Snapshot = `{}`
		if err := j.CaptureResetOperation(manual); err != nil {
			t.Fatal(err)
		}
	}
	page, err := j.ResetOperationPage("", 1)
	if err != nil || len(page) != 1 || page[0].RequestID != "a-first" {
		t.Fatalf("first bounded page: %+v/%v", page, err)
	}
	page, err = j.ResetOperationPage("a-first", 1)
	digest := sha256.Sum256([]byte(snapshot))
	want := ResetOperationSummary{RequestID: r.RequestID, SourceID: r.SourceID, CalendarKey: calendar, SnapshotBytes: uint64(len(snapshot)), Digest: hex.EncodeToString(digest[:])}
	if err != nil || len(page) != 1 || page[0] != want {
		t.Fatalf("header-only next page: %+v/%v", page, err)
	}
	page, err = j.ResetOperationPage("original-request", 128)
	if err != nil || len(page) != 1 || page[0].RequestID != "z-final" {
		t.Fatalf("last page: %+v/%v", page, err)
	}
	page, err = j.ResetOperationPage("z-final", 128)
	if err != nil || len(page) != 0 {
		t.Fatalf("page beyond final: %+v/%v", page, err)
	}
	for _, limit := range []int{0, 129} {
		if _, err := j.ResetOperationPage("", limit); !errors.Is(err, ErrRequest) {
			t.Fatalf("unbounded page: %v", err)
		}
	}
	if _, err := j.LookupResetOperation("absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent operation: %v", err)
	}
	if _, err := j.LookupResetCalendar(strings.Repeat("c", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent calendar: %v", err)
	}
	after, err := j.Account("canonical-client")
	if err != nil || after != account {
		t.Fatalf("capture changed accounting: exact=%v err=%v", after == account, err)
	}
	retained, err := j.Issue(grant.Request)
	if err != nil || retained != grant {
		t.Fatalf("capture changed funded grant: exact=%v err=%v", retained == grant, err)
	}
}

func TestResetOperationCaptureRetainsValidJSONNumbersAcrossReopen(t *testing.T) {
	for _, snapshot := range []string{`{"n":1e400}`, `{"n":-1e400}`, `{"nested":[{"n":1e-400}]}`, `{"n":` + strings.Repeat("9", 400) + `}`} {
		t.Run(snapshot[:min(24, len(snapshot))], func(t *testing.T) {
			j, id, grant, path := resetCaptureFixture(t)
			account, err := j.Account("canonical-client")
			if err != nil {
				t.Fatal(err)
			}
			r := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "valid-number", Snapshot: snapshot}
			if err := j.CaptureResetOperation(r); err != nil {
				t.Fatalf("valid JSON number rejected at capture: %v", err)
			}
			got, err := j.LookupResetOperation(r.RequestID)
			if err != nil || got != r {
				t.Fatalf("number changed at lookup: exact=%v err=%v", got == r, err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			j, err = Open(path, id)
			if err != nil {
				t.Fatalf("valid JSON capture prevents journal reopening: %v", err)
			}
			defer j.Close()
			got, err = j.LookupResetOperation(r.RequestID)
			if err != nil || got != r {
				t.Fatalf("number changed after reopen: exact=%v err=%v", got == r, err)
			}
			if err := j.CaptureResetOperation(r); err != nil {
				t.Fatalf("valid number exact retry: %v", err)
			}
			after, err := j.Account("canonical-client")
			if err != nil || after != account {
				t.Fatalf("number capture changed account: exact=%v err=%v", after == account, err)
			}
			retained, err := j.Grant(grant.GrantID)
			if err != nil || retained != grant {
				t.Fatalf("number capture changed grant: exact=%v err=%v", retained == grant, err)
			}
		})
	}
}

func TestResetOperationCaptureRecordExhaustionPreservesFundedState(t *testing.T) {
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	r := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "original-request", Snapshot: `{}`}
	if err := j.CaptureResetOperation(r); err != nil {
		t.Fatal(err)
	}
	// Populate the actual production bound in one fixture transaction. Every
	// header/chunk pair is a complete independently readable capture.
	err = j.db.Update(func(tx *bolt.Tx) error {
		var original resetOperationHeader
		if err := get(tx, "reset-operations", "h/original-request", &original); err != nil {
			return err
		}
		for i := 0; i < 49999; i++ {
			h := original
			h.RequestID = fmt.Sprintf("retained-%05d", i)
			if err := put(tx, "reset-operations", "h/"+h.RequestID, h); err != nil {
				return err
			}
			if err := put(tx, "reset-operations", resetChunkKey(h.RequestID, 0), resetOperationChunk{Data: []byte(`{}`)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.RequestID = "overflow"
	if err := j.CaptureResetOperation(changed); !errors.Is(err, ErrJournal) {
		t.Fatalf("record exhaustion accepted: %v", err)
	}
	if err := j.CaptureResetOperation(r); err != nil {
		t.Fatalf("existing retry at capacity: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.LookupResetOperation("overflow"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial overflow capture: %v", err)
	}
	for _, request := range []string{"original-request", "retained-00000", "retained-49998"} {
		got, err := j.LookupResetOperation(request)
		if err != nil || got.Snapshot != `{}` || got.RequestID != request {
			t.Fatalf("retained capture at limit changed: %v", err)
		}
	}
	after, err := j.Account("canonical-client")
	if err != nil || after != account {
		t.Fatalf("exhaustion changed account: exact=%v err=%v", after == account, err)
	}
	retained, err := j.Grant(grant.GrantID)
	if err != nil || retained != grant {
		t.Fatalf("exhaustion changed grant: exact=%v err=%v", retained == grant, err)
	}
}

func TestResetOperationCaptureFileExhaustionPreservesFundedState(t *testing.T) {
	j, id, grant, path := resetCaptureFixture(t)
	account, err := j.Account("canonical-client")
	if err != nil {
		t.Fatal(err)
	}
	original := ResetOperationCapture{Identity: id, SourceID: "reset-source", RequestID: "original-request", Snapshot: `{"targets":["original-client"]}`}
	if err := j.CaptureResetOperation(original); err != nil {
		t.Fatal(err)
	}
	// A test-only opaque bucket occupies actual file pages. This exercises the
	// production 256 MiB allocator bound without making race instrumentation
	// repeatedly decode 64 MiB JSON tokens. No production limits are reduced.
	err = j.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("test-file-pressure"))
		if err != nil {
			return err
		}
		return b.Put([]byte("retained-pressure"), make([]byte, 239<<20))
	})
	if err != nil {
		t.Fatalf("could not establish real file pressure: %v", err)
	}
	candidate := original
	candidate.RequestID = "overflow"
	candidate.Snapshot = `{"targets":"` + strings.Repeat("a", 16<<20) + `"}`
	if err := j.CaptureResetOperation(candidate); !errors.Is(err, ErrJournal) || !errors.Is(err, berrors.ErrMaxSizeReached) {
		t.Fatalf("actual file exhaustion accepted or wrong failure: %v", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() > 256<<20 {
		t.Fatalf("journal exceeded file bound: %v", err)
	}
	j, err = Open(path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err := j.LookupResetOperation(candidate.RequestID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed capture persisted: %v", err)
	}
	page, err := j.ResetOperationPage("", 128)
	if err != nil || len(page) != 1 || page[0].RequestID != original.RequestID {
		t.Fatalf("committed headers changed: %d/%v", len(page), err)
	}
	got, err := j.LookupResetOperation(original.RequestID)
	if err != nil || got != original {
		t.Fatalf("original capture changed: exact=%v err=%v", got == original, err)
	}
	after, err := j.Account("canonical-client")
	if err != nil || after != account {
		t.Fatalf("file exhaustion changed account: exact=%v err=%v", after == account, err)
	}
	retained, err := j.Grant(grant.GrantID)
	if err != nil || retained != grant {
		t.Fatalf("file exhaustion changed grant: exact=%v err=%v", retained == grant, err)
	}
}
