package distribution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPromotionRejectsInvalidCandidateAndUnsafeBackupBeforeOldTreeChanges(t *testing.T) {
	for _, mode := range []string{"invalid-candidate", "same-path", "nested-backup", "existing-backup", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			parent := t.TempDir()
			old := filepath.Join(parent, "installed")
			if err := os.Mkdir(old, 0700); err != nil {
				t.Fatal(err)
			}
			panel := filepath.Join(old, "x-ui")
			if err := os.WriteFile(panel, []byte("old usable installation"), 0700); err != nil {
				t.Fatal(err)
			}
			candidate, _ := manifestFixture(t)
			backup := filepath.Join(parent, "retained-previous")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "same-path":
				backup = old
			case "nested-backup":
				backup = filepath.Join(old, "previous")
			case "existing-backup":
				if err := os.Mkdir(backup, 0700); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			}
			if _, err := Promote(ctx, candidate, old, backup); err == nil {
				t.Fatalf("accepted %s promotion", mode)
			}
			after, err := os.ReadFile(panel)
			if err != nil || string(after) != "old usable installation" {
				t.Fatal("rejected promotion changed installed resources")
			}
		})
	}
}
