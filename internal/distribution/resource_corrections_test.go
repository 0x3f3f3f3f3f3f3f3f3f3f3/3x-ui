package distribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func correctionPackage(t *testing.T, root, revision string, resources map[string]string) Manifest {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	report := BinaryReport{FormatVersion: 1, APIVersion: 1, Compatibility: "traffic-control-v1", SourceRevision: revision, Target: CurrentTarget(), GoVersion: GoToolchain, Capabilities: RequiredCapabilities()}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"x-ui", "bin/" + CoreBinaryName(report.Target.OS, report.Target.Arch)} {
		resources[name] = "#!/bin/sh\nprintf '%s\\n' '" + string(data) + "'\n"
	}
	for name, value := range resources {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0700); err != nil {
			t.Fatal(err)
		}
	}
	m, err := Generate(root, report.Target, revision, map[string]string{"go": GoToolchain, "node": NodeToolchain})
	if err != nil {
		t.Fatal(err)
	}
	return *m
}

func TestInstalledVerificationAcceptsRotatedGeodataButIncomingRemainsStrict(t *testing.T) {
	root := t.TempDir()
	correctionPackage(t, root, strings.Repeat("a", 40), map[string]string{"bin/geoip.dat": "distributed geodata", "licenses/source.txt": "source receipt"})
	if _, err := VerifyIncoming(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin/geoip.dat"), []byte("custom rotating resource"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFiles(root); err != nil {
		t.Fatalf("installed geodata rotation invalidated immutable pair: %v", err)
	}
	if _, err := Verify(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyIncoming(context.Background(), root); err == nil {
		t.Fatal("incoming geodata accepted without its original hash")
	}
	if err := os.WriteFile(filepath.Join(root, "licenses/source.txt"), []byte("tampered source receipt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyFiles(root); err == nil {
		t.Fatal("installed verification accepted changed immutable license")
	}
}

func TestPromotionKeepsCustomizedCurrentGeodataAndOriginalPackageManifest(t *testing.T) {
	for _, customized := range []bool{false, true} {
		t.Run(map[bool]string{false: "distributed", true: "customized"}[customized], func(t *testing.T) {
			parent := t.TempDir()
			installed, candidate, previous := filepath.Join(parent, "installed"), filepath.Join(parent, "candidate"), filepath.Join(parent, "previous")
			correctionPackage(t, installed, strings.Repeat("a", 40), map[string]string{"bin/geoip.dat": "old package geodata"})
			correctionPackage(t, candidate, strings.Repeat("b", 40), map[string]string{"bin/geoip.dat": "new package geodata"})
			original, err := os.ReadFile(filepath.Join(candidate, ManifestName))
			if err != nil {
				t.Fatal(err)
			}
			want := "new package geodata"
			if customized {
				want = "custom administrator geodata"
				if err := os.WriteFile(filepath.Join(installed, "bin/geoip.dat"), []byte(want), 0600); err != nil {
					t.Fatal(err)
				}
			}
			p, err := Promote(context.Background(), candidate, installed, previous)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(installed, "bin/geoip.dat"))
			if err != nil || string(got) != want {
				t.Fatalf("geodata = %q, want %q: %v", got, want, err)
			}
			after, err := os.ReadFile(filepath.Join(installed, ManifestName))
			if err != nil || string(after) != string(original) {
				t.Fatal("preservation rewrote the distributed package manifest", err)
			}
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var receipt struct {
				PackageSHA256 string `json:"packageSHA256"`
				Overrides     []File `json:"resourceOverrides"`
			}
			if err := json.Unmarshal(data, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.PackageSHA256 != resourceHash(string(original)) {
				t.Fatal("transaction lacks an independently verifiable package manifest receipt")
			}
			if customized {
				if len(receipt.Overrides) != 1 || receipt.Overrides[0] != (File{Path: "bin/geoip.dat", Role: "geodata", Size: int64(len(want)), SHA256: resourceHash(want)}) {
					t.Fatalf("customized geodata receipt is missing or inaccurate: %+v", receipt.Overrides)
				}
			} else if len(receipt.Overrides) != 0 {
				t.Fatalf("unchanged distributed geodata reported as an override: %+v", receipt.Overrides)
			}
		})
	}
}

func TestRollbackUsesBothResourceManifestsAndOnlyCurrentState(t *testing.T) {
	parent := t.TempDir()
	installed, candidate, previous, failed := filepath.Join(parent, "installed"), filepath.Join(parent, "candidate"), filepath.Join(parent, "previous"), filepath.Join(parent, "failed")
	correctionPackage(t, installed, strings.Repeat("a", 40), map[string]string{"licenses/old-resource.txt": "old immutable distribution resource", "bin/geoip.dat": "old packaged geodata"})
	correctionPackage(t, candidate, strings.Repeat("b", 40), map[string]string{"bin/geoip.dat": "new packaged geodata"})
	for name, value := range map[string]string{"ledger": "spent=5", "deleted-user.key": "deleted credential"} {
		if err := os.WriteFile(filepath.Join(installed, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Promote(context.Background(), candidate, installed, previous); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "ledger"), []byte("spent=900"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(installed, "deleted-user.key")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "bin/geoip.dat"), []byte("current custom geodata"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(context.Background(), installed, failed); err != nil {
		t.Fatalf("resource removed from new manifest blocked code rollback: %v", err)
	}
	for name, want := range map[string]string{"ledger": "spent=900", "licenses/old-resource.txt": "old immutable distribution resource", "bin/geoip.dat": "current custom geodata"} {
		got, err := os.ReadFile(filepath.Join(installed, name))
		if err != nil || string(got) != want {
			t.Fatalf("rollback %s = %q, want %q: %v", name, got, want, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(installed, "deleted-user.key")); !os.IsNotExist(err) {
		t.Fatal("resurrected a deleted credential", err)
	}
	for _, tree := range []string{previous, failed} {
		want := map[string]string{previous: "spent=5", failed: "spent=900"}[tree]
		got, err := os.ReadFile(filepath.Join(tree, "ledger"))
		if err != nil || string(got) != want {
			t.Fatal("retained tree changed", tree, string(got), err)
		}
	}
}

func TestRecoverRetainsOrphanJournalAndCanBeRepeated(t *testing.T) {
	parent := t.TempDir()
	installed, previous := filepath.Join(parent, "installed"), filepath.Join(parent, "previous")
	if err := os.Mkdir(previous, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"x-ui": "old executable", "ledger": "spent=900"} {
		if err := os.WriteFile(filepath.Join(previous, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := &Promotion{SourceRevision: strings.Repeat("a", 40), Installed: installed, Previous: previous, Candidate: filepath.Join(parent, "candidate"), State: "prepared"}
	if err := writePromotionJournal(installed+".pending.json", p); err != nil {
		t.Fatal(err)
	}
	orphan := previous + ".transaction.json.tmp"
	if err := os.WriteFile(orphan, []byte("{partial interrupted write"), 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := Recover(context.Background(), installed); err != nil {
			t.Fatalf("recovery attempt %d obstructed by orphan temporary journal: %v", attempt, err)
		}
	}
	got, err := os.ReadFile(orphan)
	if err != nil || string(got) != "{partial interrupted write" {
		t.Fatal("orphan evidence changed", err)
	}
	got, err = os.ReadFile(filepath.Join(installed, "ledger"))
	if err != nil || string(got) != "spent=900" {
		t.Fatal("recovery changed current state", err)
	}
}

func resourceHash(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:])
}

func TestInstalledMutableGeodataRejectsUnsafeOrUnboundedResources(t *testing.T) {
	for _, kind := range []string{"empty", "oversized", "link", "parent-link", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			correctionPackage(t, root, strings.Repeat("a", 40), map[string]string{"bin/geoip.dat": "packaged geodata"})
			name := filepath.Join(root, "bin/geoip.dat")
			switch kind {
			case "empty":
				if err := os.Truncate(name, 0); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.Truncate(name, maxFileBytes+1); err != nil {
					t.Fatal(err)
				}
			case "link":
				if err := os.Rename(name, name+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("geoip.dat.original", name); err != nil {
					t.Fatal(err)
				}
			case "parent-link":
				if err := os.Rename(filepath.Join(root, "bin"), filepath.Join(root, "old-bin")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("old-bin", filepath.Join(root, "bin")); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(name, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := VerifyFiles(root); err == nil {
				t.Fatalf("accepted %s mutable resource", kind)
			}
		})
	}
}

func TestRollbackLegacyResourcesCannotCollideWithCurrentState(t *testing.T) {
	parent := t.TempDir()
	installed, candidate, previous, failed := filepath.Join(parent, "installed"), filepath.Join(parent, "candidate"), filepath.Join(parent, "previous"), filepath.Join(parent, "failed")
	if err := os.MkdirAll(filepath.Join(installed, "licenses"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"x-ui": "legacy panel", "licenses/old-resource.txt": "legacy immutable resource", "ledger": "spent=5", "deleted-user.key": "stale credential"} {
		if err := os.WriteFile(filepath.Join(installed, name), []byte(value), 0700); err != nil {
			t.Fatal(err)
		}
	}
	correctionPackage(t, candidate, strings.Repeat("b", 40), map[string]string{"bin/geoip.dat": "distributed geodata"})
	if _, err := Promote(context.Background(), candidate, installed, previous); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "ledger"), []byte("spent=900"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(installed, "deleted-user.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(context.Background(), installed, failed); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"x-ui": "legacy panel", "licenses/old-resource.txt": "legacy immutable resource", "ledger": "spent=900"} {
		got, err := os.ReadFile(filepath.Join(installed, name))
		if err != nil || string(got) != want {
			t.Fatal(name, string(got), err)
		}
	}
	if _, err := os.Lstat(filepath.Join(installed, "deleted-user.key")); !os.IsNotExist(err) {
		t.Fatal("legacy rollback restored a deleted credential", err)
	}
}

func TestJournalRefusesReceiptTooLargeForRecoveryBeforeWriting(t *testing.T) {
	name := filepath.Join(t.TempDir(), "transaction.json")
	p := &Promotion{Installed: strings.Repeat("x", maxManifestBytes)}
	if err := writePromotionJournal(name, p); err == nil {
		t.Fatal("wrote a transaction too large for recovery to decode")
	}
	if _, err := os.Lstat(name); !os.IsNotExist(err) {
		t.Fatal("oversized journal became visible", err)
	}
}

func TestPromotionRejectsChangedIncomingGeodataBeforeChangingCurrentTree(t *testing.T) {
	parent := t.TempDir()
	installed, candidate, previous := filepath.Join(parent, "installed"), filepath.Join(parent, "candidate"), filepath.Join(parent, "previous")
	correctionPackage(t, installed, strings.Repeat("a", 40), map[string]string{})
	correctionPackage(t, candidate, strings.Repeat("b", 40), map[string]string{"bin/geoip.dat": "incoming distributed geodata"})
	if err := os.WriteFile(filepath.Join(installed, "ledger"), []byte("spent=900"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "bin/geoip.dat"), []byte("changed incoming data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Promote(context.Background(), candidate, installed, previous); err == nil {
		t.Fatal("promotion used mutable installed verification for an incoming package")
	}
	got, err := os.ReadFile(filepath.Join(installed, "ledger"))
	if err != nil || string(got) != "spent=900" {
		t.Fatal("rejected incoming resource changed current state", err)
	}
	if _, err := os.Lstat(previous); !os.IsNotExist(err) {
		t.Fatal("rejected incoming resource moved the previous tree", err)
	}
}
