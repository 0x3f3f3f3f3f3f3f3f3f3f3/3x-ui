package distribution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverInterruptedOldRenameKeepsStateAndRetainsJournal(t *testing.T) {
	parent := t.TempDir()
	installed, previous := filepath.Join(parent, "installed"), filepath.Join(parent, "previous")
	if err := os.Mkdir(previous, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"x-ui": "old binary", "billing-state": "spent=123456", "business.key": "fixture key"} {
		if err := os.WriteFile(filepath.Join(previous, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := Promotion{SourceRevision: strings.Repeat("a", 40), Installed: installed, Previous: previous, Candidate: filepath.Join(parent, "candidate"), State: "prepared"}
	// The process can die after the first rename, before recording its new state.
	data, _ := json.Marshal(p)
	if err := os.WriteFile(installed+".pending.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := Recover(context.Background(), installed)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "recovered-previous" {
		t.Fatal(got)
	}
	for name, data := range map[string]string{"x-ui": "old binary", "billing-state": "spent=123456", "business.key": "fixture key"} {
		actual, err := os.ReadFile(filepath.Join(installed, name))
		if err != nil || string(actual) != data {
			t.Fatal(name, err)
		}
	}
	if _, err := os.Stat(previous + ".transaction.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installed + ".pending.json"); !os.IsNotExist(err) {
		t.Fatal("pending transaction remains", err)
	}
}

func TestRecoveryRejectsForeignOrAmbiguousJournalWithoutChangingTrees(t *testing.T) {
	for _, mode := range []string{"foreign-path", "nested-backup", "both-missing", "linked-journal", "duplicate-json"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			installed := filepath.Join(parent, "installed")
			previous := filepath.Join(parent, "previous")
			p := Promotion{SourceRevision: strings.Repeat("a", 40), Installed: installed, Previous: previous, Candidate: filepath.Join(parent, "candidate"), State: "prepared"}
			if mode == "foreign-path" {
				p.Installed = filepath.Join(parent, "other")
			}
			if mode == "nested-backup" {
				p.Previous = filepath.Join(installed, "backup")
			}
			data, _ := json.Marshal(p)
			journal := installed + ".pending.json"
			if mode == "duplicate-json" {
				data = []byte(`{"installed":"a","installed":"b"}`)
			}
			if err := os.WriteFile(journal, data, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "linked-journal" {
				if err := os.Rename(journal, journal+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(journal+".original", journal); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Recover(context.Background(), installed); err == nil {
				t.Fatal("accepted unsafe recovery")
			}
			if _, err := os.Lstat(journal); err != nil {
				t.Fatal("failure removed recovery evidence")
			}
		})
	}
}

func TestInterruptedRollbackRecoversLatestStateInsteadOfPreviousBackup(t *testing.T) {
	parent := t.TempDir()
	installed := filepath.Join(parent, "installed")
	p := Promotion{SourceRevision: strings.Repeat("a", 40), Installed: installed, Previous: filepath.Join(parent, "previous"), Candidate: filepath.Join(parent, "candidate"), State: "rollback-prepared", RollbackCandidate: filepath.Join(parent, "rollback"), Failed: filepath.Join(parent, "failed")}
	for _, name := range []string{p.Previous, p.RollbackCandidate, p.Failed} {
		if err := os.Mkdir(name, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(name, "x-ui"), []byte("code"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(p.Previous, "ledger"), []byte("spent=5"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.RollbackCandidate, "ledger"), []byte("spent=900"), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(p)
	if err := os.WriteFile(installed+".pending.json", data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(context.Background(), installed); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(installed, "ledger"))
	if err != nil || string(got) != "spent=900" {
		t.Fatal(string(got), err)
	}
	old, _ := os.ReadFile(filepath.Join(p.Previous, "ledger"))
	if string(old) != "spent=5" {
		t.Fatal("previous backup changed")
	}
}
