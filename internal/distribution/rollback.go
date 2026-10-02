package distribution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Only old executable/distribution resources can enter a rollback candidate.
// All database, ledger, credential and runtime files come from the stopped
// CURRENT installation. Missing current state is never resurrected from backup.
func copyRollbackCode(previous, candidate string) error {
	files := []string{}
	if _, err := os.Lstat(filepath.Join(previous, ManifestName)); err == nil {
		m, err := VerifyFiles(previous)
		if err != nil {
			return err
		}
		files = append(files, ManifestName)
		for _, f := range m.Files {
			files = append(files, f.Path)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		err := filepath.WalkDir(previous, func(name string, e fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(previous, name)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			if e.IsDir() {
				if rel == "bin" || rel == "licenses" || strings.HasPrefix(rel, "licenses/") || rel == "internal" || rel == "internal/web" || rel == "internal/web/translation" || strings.HasPrefix(rel, "internal/web/translation/") {
					return nil
				}
				return fs.SkipDir
			}
			if _, err := packageRole(rel, CurrentTarget()); err == nil || rel == "x-ui.service" {
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, name := range files {
		path, info, err := regularFile(previous, name)
		if err != nil {
			return err
		}
		if info.Size() > maxFileBytes {
			return errors.New("old code resource is too large")
		}
		if err := os.MkdirAll(filepath.Join(candidate, filepath.Dir(name)), 0700); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(filepath.Join(candidate, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		n, copyErr := io.Copy(output, io.LimitReader(input, info.Size()+1))
		syncErr := output.Sync()
		sourceErr, targetErr := input.Close(), output.Close()
		if copyErr != nil || syncErr != nil || sourceErr != nil || targetErr != nil || n != info.Size() {
			return fmt.Errorf("old code resource copy failed: %s", name)
		}
	}
	if _, _, err := regularFile(candidate, "x-ui"); err != nil {
		return errors.New("rollback candidate lacks its old panel")
	}
	return nil
}

// Rollback restores only code. It retains both the original backup and the
// failed new tree, and leaves any database schema migration in place.
func Rollback(ctx context.Context, installed, failed string) (*Promotion, error) {
	installed, err := filepath.Abs(installed)
	if err != nil {
		return nil, err
	}
	failed, err = filepath.Abs(failed)
	if err != nil {
		return nil, err
	}
	if filepath.Dir(failed) != filepath.Dir(installed) || failed == installed {
		return nil, errors.New("failed tree must have a distinct sibling path")
	}
	release, err := lockPromotion(installed)
	if err != nil {
		return nil, err
	}
	defer release()
	p, err := readPending(installed)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("no pending installation to roll back")
	}
	if failed == p.Previous || containsPath(failed, p.Candidate) || containsPath(p.Candidate, failed) {
		return nil, errors.New("failed tree conflicts with retained resources")
	}
	if _, err := os.Lstat(failed); !os.IsNotExist(err) {
		return nil, errors.New("failed tree already exists")
	}
	if ok, err := installedTree(p.Previous); err != nil || !ok {
		return nil, errors.New("no retained old installation for code rollback")
	}
	m, err := Verify(ctx, installed)
	if err != nil {
		return nil, err
	}
	if m.SourceRevision != p.SourceRevision {
		return nil, errors.New("installed pair differs from pending source")
	}
	candidate, err := os.MkdirTemp(filepath.Dir(installed), ".x-ui-code-rollback-")
	if err != nil {
		return nil, err
	}
	if err := copyRollbackCode(p.Previous, candidate); err != nil {
		return nil, err
	}
	if err := PreserveResources(installed, candidate, m); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.RollbackCandidate, p.Failed, p.State = candidate, failed, "rollback-prepared"
	if err := writePromotionJournal(installed+".pending.json", p); err != nil {
		return nil, err
	}
	if err := os.Rename(installed, failed); err != nil {
		return nil, err
	}
	if err := os.Rename(candidate, installed); err != nil {
		restoreErr := os.Rename(failed, installed)
		return nil, fmt.Errorf("code rollback rename: %v; current tree restore: %v", err, restoreErr)
	}
	p.State = "rolled-back-code"
	if err := writePromotionJournal(p.Previous+".transaction.json", p); err != nil {
		return nil, err
	}
	if err := os.Remove(installed + ".pending.json"); err != nil {
		return nil, err
	}
	return p, nil
}
