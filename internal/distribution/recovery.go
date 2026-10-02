package distribution

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func verifyPendingPackage(ctx context.Context, p *Promotion) (*Manifest, error) {
	m, err := Verify(ctx, p.Installed)
	if err != nil {
		return nil, err
	}
	if m.SourceRevision != p.SourceRevision {
		return nil, errors.New("installed pair differs from pending source")
	}
	if p.PackageSHA256 != "" {
		receipt, err := snapshotResource(p.Installed, ManifestName, "manifest", maxManifestBytes)
		if err != nil || receipt.SHA256 != p.PackageSHA256 {
			return nil, errors.New("installed package differs from pending manifest")
		}
	}
	return m, nil
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
		if err := renamePromotionPath(p.Previous, installed); err != nil {
			return nil, err
		}
		p.State = "recovered-previous"
	} else if retained || p.State == "promoted" {
		if _, err := verifyPendingPackage(ctx, p); err != nil {
			return nil, err
		}
		p.State = "promoted"
		return p, nil
	} else if p.State == "prepared" {
		candidate, err := installedTree(p.Candidate)
		if err != nil {
			return nil, err
		}
		if !candidate {
			// Fresh rename completed before its promoted journal write. The
			// installed code still requires actual activation, never completion.
			if _, err := verifyPendingPackage(ctx, p); err != nil {
				return nil, err
			}
			p.State = "promoted"
			if err := writePromotionJournal(installed+".pending.json", p); err != nil {
				return nil, err
			}
			return p, nil
		}
		p.State = "recovered-unmodified"
	} else {
		return nil, errors.New("pending transaction is ambiguous")
	}
	if err := writePromotionJournal(p.Previous+".transaction.json", p); err != nil {
		return nil, err
	}
	if err := removePromotionJournal(installed + ".pending.json"); err != nil {
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
		if _, err := verifyPendingPackage(ctx, p); err != nil {
			return nil, err
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
		if err := renamePromotionPath(p.RollbackCandidate, p.Installed); err != nil {
			return nil, err
		}
	} else if !installed && !candidate && failed {
		if err := renamePromotionPath(p.Failed, p.Installed); err != nil {
			return nil, err
		}
		p.State = "promoted"
		p.RollbackCandidate, p.Failed = "", ""
		if err := writePromotionJournal(p.Installed+".pending.json", p); err != nil {
			return nil, err
		}
		return p, nil
	} else if !(installed && !candidate && failed) {
		return nil, errors.New("rollback recovery tree state is ambiguous")
	}
	p.State = "rolled-back-code"
	if err := writePromotionJournal(p.Previous+".transaction.json", p); err != nil {
		return nil, err
	}
	if err := removePromotionJournal(p.Installed + ".pending.json"); err != nil {
		return nil, err
	}
	return p, nil
}

// CompletePromotion records successful activation after the caller checks the
// service. Retained trees and per-transaction receipts are kept indefinitely.
func CompletePromotion(ctx context.Context, installed, healthPath string) (*Promotion, error) {
	return finishPromotion(ctx, installed, healthPath, "activated")
}

func CompleteOfflinePromotion(ctx context.Context, installed string) (*Promotion, error) {
	return finishPromotion(ctx, installed, "", "installed-offline")
}

func finishPromotion(ctx context.Context, installed, healthPath, state string) (*Promotion, error) {
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
	if p.State != "promoted" {
		return nil, errors.New("installation requires recovery before completion")
	}
	if _, err := verifyPendingPackage(ctx, p); err != nil {
		return nil, err
	}
	if state == "activated" {
		if err := WaitRuntimeHealth(ctx, installed, healthPath); err != nil {
			return nil, err
		}
	}
	p.State = state
	if err := writePromotionJournal(p.Previous+".transaction.json", p); err != nil {
		return nil, err
	}
	if err := removePromotionJournal(installed + ".pending.json"); err != nil {
		return nil, err
	}
	return p, nil
}

// RecoveryWork returns only the original private installer workspace, never a
// path supplied by an unvalidated shell JSON parser. The snapshot marker proves
// control backups were finished before this transaction stopped the service.
func RecoveryWork(installed string) (string, error) {
	installed, err := filepath.Abs(installed)
	if err != nil {
		return "", err
	}
	p, err := readPending(installed)
	if err != nil || p == nil {
		return "", err
	}
	work := filepath.Dir(p.Candidate)
	if filepath.Base(work) == "staged" {
		work = filepath.Dir(work)
	}
	if filepath.Dir(work) != filepath.Dir(installed) || !strings.HasPrefix(filepath.Base(work), ".x-ui-paired.") {
		return "", errors.New("pending transaction has no private installer workspace")
	}
	info, err := os.Lstat(work)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("pending installer workspace is missing or unsafe")
	}
	if _, _, err := regularFile(work, "controls-snapshot"); err != nil {
		return "", errors.New("original control snapshot is incomplete; retain transaction for recovery")
	}
	return work, nil
}
