package distribution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func readPending(installed string) (*Promotion, error) {
	name := installed + ".pending.json"
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxManifestBytes {
		return nil, errors.New("invalid pending installation journal")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	var p Promotion
	if err := decodeStrict(data, &p); err != nil {
		return nil, err
	}
	if p.Installed != installed || !revisionPattern.MatchString(p.SourceRevision) || !filepath.IsAbs(p.Previous) || filepath.Clean(p.Previous) != p.Previous || filepath.Dir(p.Previous) != filepath.Dir(installed) || p.Previous == installed || !filepath.IsAbs(p.Candidate) || filepath.Clean(p.Candidate) != p.Candidate || containsPath(installed, p.Candidate) || containsPath(p.Candidate, installed) || containsPath(p.Previous, p.Candidate) || containsPath(p.Candidate, p.Previous) {
		return nil, errors.New("pending journal paths or source are invalid")
	}
	switch p.State {
	case "prepared", "previous-retained", "promoted":
		if p.RollbackCandidate != "" || p.Failed != "" {
			return nil, errors.New("unexpected rollback paths")
		}
	case "rollback-prepared":
		for _, name := range []string{p.RollbackCandidate, p.Failed} {
			if !filepath.IsAbs(name) || filepath.Clean(name) != name || filepath.Dir(name) != filepath.Dir(installed) || name == installed || name == p.Previous {
				return nil, errors.New("invalid rollback recovery path")
			}
		}
		if p.RollbackCandidate == p.Failed {
			return nil, errors.New("rollback paths collide")
		}
	default:
		return nil, errors.New("unknown pending installation state")
	}
	return &p, nil
}

func installedTree(name string) (bool, error) {
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("installation recovery tree is not a directory without a link")
	}
	if _, _, err := regularFile(name, "x-ui"); err != nil {
		return false, err
	}
	return true, nil
}

// Recover restores a retained old tree only when the installation path is
// missing. A promoted pair is validated and reported for the caller to finish
// activating; its live database and policy state are never restored from backup.
// The caller must hold its lifecycle lock and stop the service first.
func Recover(ctx context.Context, installed string) (*Promotion, error) {
	installed, err := filepath.Abs(installed)
	if err != nil {
		return nil, err
	}
	release, err := lockPromotion(installed)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := readPending(installed)
	if err != nil || p == nil {
		return p, err
	}
	if p.State == "rollback-prepared" {
		return recoverRollback(ctx, p)
	}
	exists, err := installedTree(installed)
	if err != nil {
		return nil, err
	}
	retained, err := installedTree(p.Previous)
	if err != nil {
		return nil, err
	}
	if !exists && !retained {
		return nil, errors.New("pending installation has neither installed nor retained panel")
	}
	if !exists {
		if err := os.Rename(p.Previous, installed); err != nil {
			return nil, err
		}
		p.State = "recovered-previous"
	} else if retained || p.State == "promoted" {
		m, err := Verify(ctx, installed)
		if err != nil {
			return nil, err
		}
		if m.SourceRevision != p.SourceRevision {
			return nil, errors.New("installed pair differs from the pending transaction")
		}
		p.State = "promoted"
		return p, nil
	} else if p.State == "prepared" {
		p.State = "recovered-unmodified"
	} else {
		return nil, errors.New("pending transaction is ambiguous")
	}
	if err := writePromotionJournal(p.Previous+".transaction.json", p); err != nil {
		return nil, err
	}
	if err := os.Remove(installed + ".pending.json"); err != nil {
		return nil, err
	}
	return p, nil
}

func recoverRollback(ctx context.Context, p *Promotion) (*Promotion, error) {
	installed, err := installedTree(p.Installed)
	if err != nil {
		return nil, err
	}
	candidate, err := installedTree(p.RollbackCandidate)
	if err != nil {
		return nil, err
	}
	failed, err := installedTree(p.Failed)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if installed && candidate && !failed {
		// Death before moving the new installation: preserve the current pair.
		m, err := Verify(ctx, p.Installed)
		if err != nil {
			return nil, err
		}
		if m.SourceRevision != p.SourceRevision {
			return nil, errors.New("rollback recovery current source mismatch")
		}
		p.State = "promoted"
		p.RollbackCandidate, p.Failed = "", ""
		if err := writePromotionJournal(p.Installed+".pending.json", p); err != nil {
			return nil, err
		}
		return p, nil
	}
	if !installed && candidate && failed {
		// This candidate already contains CURRENT state. Never use the stale
		// retained previous tree after starting a code rollback.
		if err := os.Rename(p.RollbackCandidate, p.Installed); err != nil {
			return nil, err
		}
	} else if !installed && !candidate && failed {
		if err := os.Rename(p.Failed, p.Installed); err != nil {
			return nil, err
		}
		p.State = "recovered-current"
	} else if !(installed && !candidate && failed) {
		return nil, errors.New("rollback recovery tree state is ambiguous")
	}
	if p.State != "recovered-current" {
		p.State = "rolled-back-code"
	}
	if err := writePromotionJournal(p.Previous+".transaction.json", p); err != nil {
		return nil, err
	}
	if err := os.Remove(p.Installed + ".pending.json"); err != nil {
		return nil, err
	}
	return p, nil
}

// CompletePromotion records successful activation after the caller checks the
// service. Retained trees and per-transaction receipts are kept indefinitely.
func CompletePromotion(ctx context.Context, installed string) (*Promotion, error) {
	installed, err := filepath.Abs(installed)
	if err != nil {
		return nil, err
	}
	release, err := lockPromotion(installed)
	if err != nil {
		return nil, err
	}
	defer release()
	p, err := readPending(installed)
	if err != nil || p == nil {
		return p, err
	}
	m, err := Verify(ctx, installed)
	if err != nil {
		return nil, err
	}
	if m.SourceRevision != p.SourceRevision {
		return nil, errors.New("activation pair differs from pending source")
	}
	p.State = "activated"
	if err := writePromotionJournal(p.Previous+".transaction.json", p); err != nil {
		return nil, err
	}
	if err := os.Remove(installed + ".pending.json"); err != nil {
		return nil, err
	}
	return p, nil
}
