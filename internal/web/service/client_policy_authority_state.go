package service

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/policyauthority"
)

var ErrAuthorityNotInitialized = errors.New("durable policy authority is missing or its migration is incomplete")

type durableAuthorityState struct {
	Journal  *policyauthority.Journal
	SourceID string
}

type authorityManifest struct {
	Schema         uint64                   `json:"schema"`
	Identity       policyauthority.Identity `json:"identity"`
	SourceID       string                   `json:"sourceId"`
	SnapshotDigest string                   `json:"snapshotDigest"`
	Phase          string                   `json:"phase"`
}

var authorityInitializationMu sync.Mutex
var publishAuthorityManifest = writeAuthorityManifest

func initializeAuthorityState(dir, sourceID string, snapshot authorityMigrationSnapshot) (*durableAuthorityState, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !validPolicySourceKey(sourceID) {
		return nil, ErrAuthorityNotInitialized
	}
	digest, err := authoritySnapshotDigest(snapshot)
	if err != nil {
		return nil, err
	}
	authorityInitializationMu.Lock()
	defer authorityInitializationMu.Unlock()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := checkAuthorityDirectory(dir); err != nil {
		return nil, err
	}
	manifest, err := readAuthorityManifest(dir)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(filepath.Join(dir, "journal.db")); !errors.Is(err, os.ErrNotExist) {
			return nil, ErrAuthorityNotInitialized
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		manifest = authorityManifest{Schema: 1, Identity: policyauthority.Identity{AuthorityID: hex.EncodeToString(nonce), Generation: 1}, SourceID: sourceID, SnapshotDigest: digest, Phase: "preparing"}
		if err := publishAuthorityManifest(dir, manifest, true); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if manifest.SourceID != sourceID || manifest.SnapshotDigest != digest {
		return nil, policyauthority.ErrIdentity
	}
	path := filepath.Join(dir, "journal.db")
	var journal *policyauthority.Journal
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if manifest.Phase != "preparing" {
			return nil, ErrAuthorityNotInitialized
		}
		journal, err = policyauthority.CreateWithMigrationSourceIdentity(path, manifest.Identity, sourceID, snapshot.Seeds, snapshot.Deleted, snapshot.Records)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		journal, err = policyauthority.Open(path, manifest.Identity)
		if err != nil {
			return nil, err
		}
	}
	if err := journal.CheckMigrationSource(sourceID, digest); err != nil {
		_ = journal.Close()
		return nil, err
	}
	if manifest.Phase == "preparing" {
		manifest.Phase = "committed"
		if err := publishAuthorityManifest(dir, manifest, false); err != nil {
			_ = journal.Close()
			return nil, err
		}
	}
	return &durableAuthorityState{Journal: journal, SourceID: sourceID}, nil
}
func openAuthorityState(dir string) (*durableAuthorityState, error) {
	if err := checkAuthorityDirectory(dir); err != nil {
		return nil, err
	}
	manifest, err := readAuthorityManifest(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest: %w", ErrAuthorityNotInitialized, err)
	}
	if manifest.Phase != "committed" {
		return nil, ErrAuthorityNotInitialized
	}
	journal, err := policyauthority.Open(filepath.Join(dir, "journal.db"), manifest.Identity)
	if err != nil {
		return nil, fmt.Errorf("%w: journal: %w", ErrAuthorityNotInitialized, err)
	}
	if err := journal.CheckMigrationSource(manifest.SourceID, manifest.SnapshotDigest); err != nil {
		_ = journal.Close()
		return nil, fmt.Errorf("%w: migration binding: %w", ErrAuthorityNotInitialized, err)
	}
	return &durableAuthorityState{Journal: journal, SourceID: manifest.SourceID}, nil
}

func checkAuthorityDirectory(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return ErrAuthorityNotInitialized
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrAuthorityNotInitialized
	}
	return nil
}

func readAuthorityManifest(dir string) (authorityManifest, error) {
	var manifest authorityManifest
	path := filepath.Join(dir, "authority.json")
	info, err := os.Lstat(path)
	if err != nil {
		return manifest, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() == 0 || info.Size() > 4096 {
		return manifest, ErrAuthorityNotInitialized
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, ErrAuthorityNotInitialized
	}
	if decoder.Decode(new(any)) != io.EOF || manifest.Schema != 1 || manifest.Identity.Generation != 1 || len(manifest.Identity.AuthorityID) != 32 || !validPolicySourceKey(manifest.SourceID) || len(manifest.SnapshotDigest) != 64 || manifest.Phase != "preparing" && manifest.Phase != "committed" {
		return manifest, ErrAuthorityNotInitialized
	}
	if _, err := hex.DecodeString(manifest.Identity.AuthorityID); err != nil {
		return manifest, ErrAuthorityNotInitialized
	}
	if _, err := hex.DecodeString(manifest.SnapshotDigest); err != nil {
		return manifest, ErrAuthorityNotInitialized
	}
	return manifest, nil
}

func writeAuthorityManifest(dir string, manifest authorityManifest, create bool) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "authority.json")
	var file *os.File
	if create {
		file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	} else {
		file, err = os.CreateTemp(dir, ".authority-commit-")
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if !create {
		if err := os.Rename(file.Name(), path); err != nil {
			return err
		}
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func authoritySnapshotDigest(snapshot authorityMigrationSnapshot) (string, error) {
	return policyauthority.MigrationSnapshotDigest(snapshot.Seeds, snapshot.Deleted, snapshot.Records)
}
