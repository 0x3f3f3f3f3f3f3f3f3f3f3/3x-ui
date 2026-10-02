package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Promotion struct {
	SourceRevision string `json:"sourceRevision"`
	Installed      string `json:"installed"`
	Previous       string `json:"previous"`
	Candidate      string `json:"candidate"`
	State          string `json:"state"`
}

func containsPath(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func writePromotionJournal(name string, p *Promotion) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	temporary := name + ".tmp"
	f, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	syncErr, closeErr := f.Sync(), f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("write promotion recovery journal")
	}
	return os.Rename(temporary, name)
}

// Promote validates the candidate before changing an installed path. Its caller
// must stop and settle the old service before invocation. An interrupted run
// retains the previous tree and recovery journal; a failed second rename restores
// the old path. This does not start a service or restore/downgrade a database.
func Promote(ctx context.Context, candidate, installed, previous string) (*Promotion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var err error
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return nil, err
	}
	installed, err = filepath.Abs(installed)
	if err != nil {
		return nil, err
	}
	previous, err = filepath.Abs(previous)
	if err != nil {
		return nil, err
	}
	if filepath.Dir(installed) != filepath.Dir(previous) || containsPath(candidate, installed) || containsPath(installed, candidate) || containsPath(previous, installed) || containsPath(installed, previous) || containsPath(previous, candidate) || containsPath(candidate, previous) {
		return nil, errors.New("candidate, installation and retained previous tree must be distinct; previous must share the installation parent")
	}
	releaseLock, err := lockPromotion(installed)
	if err != nil {
		return nil, err
	}
	defer releaseLock()
	parent, err := os.Lstat(filepath.Dir(installed))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("installation parent must be a directory without a link")
	}
	for _, reserved := range []string{previous, previous + ".transaction.json", previous + ".transaction.json.tmp"} {
		if _, err := os.Lstat(reserved); !os.IsNotExist(err) {
			return nil, errors.New("retained previous path or recovery journal already exists")
		}
	}
	m, err := Verify(ctx, candidate)
	if err != nil {
		return nil, err
	}
	oldInfo, statErr := os.Lstat(installed)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	if exists {
		if !oldInfo.IsDir() || oldInfo.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("installed root must be a directory without a link")
		}
		if _, _, err := regularFile(installed, "x-ui"); err != nil {
			return nil, errors.New("refusing to replace a directory without an installed panel")
		}
		if err := PreserveResources(installed, candidate, m); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := &Promotion{SourceRevision: m.SourceRevision, Installed: installed, Previous: previous, Candidate: candidate, State: "prepared"}
	journal := previous + ".transaction.json"
	if err := writePromotionJournal(journal, p); err != nil {
		return nil, err
	}
	if exists {
		if err := os.Rename(installed, previous); err != nil {
			return p, err
		}
		p.State = "previous-retained"
		if err := writePromotionJournal(journal, p); err != nil {
			restoreErr := os.Rename(previous, installed)
			return p, fmt.Errorf("record retained installation: %v; restore: %v", err, restoreErr)
		}
	}
	if err := os.Rename(candidate, installed); err != nil {
		if exists {
			if restoreErr := os.Rename(previous, installed); restoreErr != nil {
				return p, fmt.Errorf("candidate promotion: %v; previous tree retained at %s; restore: %v", err, previous, restoreErr)
			}
		}
		p.State = "rolled-back"
		_ = writePromotionJournal(journal, p)
		return p, fmt.Errorf("candidate promotion refused; original path restored: %w", err)
	}
	p.State = "promoted"
	if err := writePromotionJournal(journal, p); err != nil {
		return p, fmt.Errorf("candidate installed and previous retained, but final recovery journal failed: %w", err)
	}
	return p, nil
}
