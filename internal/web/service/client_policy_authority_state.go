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
	panelruntime "github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

var ErrAuthorityNotInitialized = errors.New("durable policy authority is missing or its migration is incomplete")

type durableAuthorityState struct {
	Journal  *policyauthority.Journal
	SourceID string
	Role     panelruntime.NodeExecutionRole
}

type authorityManifest struct {
	Schema         uint64                          `json:"schema"`
	Identity       policyauthority.Identity        `json:"identity"`
	SourceID       string                          `json:"sourceId"`
	SnapshotDigest string                          `json:"snapshotDigest"`
	Phase          string                          `json:"phase"`
	ExecutionRole  *panelruntime.NodeExecutionRole `json:"executionRole,omitempty"`
}

var authorityInitializationMu sync.Mutex
var publishAuthorityManifest = writeAuthorityManifest

func initializeAuthorityState(dir, sourceID string, snapshot authorityMigrationSnapshot) (*durableAuthorityState, error) {
	return initializeAuthorityStateWithRole(dir, sourceID, snapshot, panelruntime.NodeExecutionRole{Mode: panelruntime.NodeExecutionLocal})
}

func initializeAuthorityStateWithRole(dir, sourceID string, snapshot authorityMigrationSnapshot, role panelruntime.NodeExecutionRole) (*durableAuthorityState, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !validPolicySourceKey(sourceID) || role.Validate() != nil {
		return nil, ErrAuthorityNotInitialized
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
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err != nil || manifest.Schema == 2 {
		for _, record := range snapshot.Records {
			if record.Kind == "execution-role" {
				return nil, ErrAuthorityNotInitialized
			}
		}
		rawRole, roleErr := json.Marshal(role)
		if roleErr != nil {
			return nil, roleErr
		}
		snapshot.Records = append(append([]policyauthority.MigrationRecord(nil), snapshot.Records...), policyauthority.MigrationRecord{Kind: "execution-role", Key: sourceID, Value: rawRole})
	}
	digest, digestErr := authoritySnapshotDigest(snapshot)
	if digestErr != nil {
		return nil, digestErr
	}
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(filepath.Join(dir, "journal.db")); !errors.Is(err, os.ErrNotExist) {
			return nil, ErrAuthorityNotInitialized
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		manifest = authorityManifest{Schema: 2, Identity: policyauthority.Identity{AuthorityID: hex.EncodeToString(nonce), Generation: 1}, SourceID: sourceID, SnapshotDigest: digest, Phase: "preparing", ExecutionRole: &role}
		if err := publishAuthorityManifest(dir, manifest, true); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if manifest.SourceID != sourceID || manifest.SnapshotDigest != digest || manifest.role() != role {
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
	if err := checkAuthorityExecutionRole(journal, manifest); err != nil {
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
	return &durableAuthorityState{Journal: journal, SourceID: sourceID, Role: manifest.role()}, nil
}
func openAuthorityState(dir string) (*durableAuthorityState, error) {
	return loadAuthorityState(dir, false)
}

func resumeAuthorityState(dir, sourceID string) (*durableAuthorityState, error) {
	authorityInitializationMu.Lock()
	defer authorityInitializationMu.Unlock()
	state, err := loadAuthorityState(dir, true)
	if err != nil {
		return nil, err
	}
	if state.SourceID != sourceID {
		_ = state.Journal.Close()
		return nil, policyauthority.ErrIdentity
	}
	manifest, err := readAuthorityManifest(dir)
	if err == nil && manifest.Phase == "preparing" {
		manifest.Phase = "committed"
		err = publishAuthorityManifest(dir, manifest, false)
	}
	if err != nil {
		_ = state.Journal.Close()
		return nil, err
	}
	return state, nil
}

func loadAuthorityState(dir string, prepared bool) (*durableAuthorityState, error) {
	if err := checkAuthorityDirectory(dir); err != nil {
		return nil, err
	}
	manifest, err := readAuthorityManifest(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest: %w", ErrAuthorityNotInitialized, err)
	}
	if manifest.Phase != "committed" && !prepared {
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
	if err := checkAuthorityExecutionRole(journal, manifest); err != nil {
		_ = journal.Close()
		return nil, err
	}
	return &durableAuthorityState{Journal: journal, SourceID: manifest.SourceID, Role: manifest.role()}, nil
}

func (m authorityManifest) role() panelruntime.NodeExecutionRole {
	if m.ExecutionRole != nil {
		return *m.ExecutionRole
	}
	return panelruntime.NodeExecutionRole{Mode: panelruntime.NodeExecutionLocal}
}

// The independent journal pins the role as original migration evidence. A
// syntactically valid manifest edit or schema downgrade cannot change issuers.
func checkAuthorityExecutionRole(journal *policyauthority.Journal, manifest authorityManifest) error {
	records, err := journal.MigrationPage("execution-role", "", "", 2)
	if err != nil {
		return err
	}
	if manifest.Schema == 1 {
		if len(records) != 0 {
			return policyauthority.ErrIdentity
		}
		return nil
	}
	if len(records) != 1 || records[0].Key != manifest.SourceID {
		return policyauthority.ErrIdentity
	}
	var role panelruntime.NodeExecutionRole
	if json.Unmarshal(records[0].Value, &role) != nil || role != manifest.role() || role.Validate() != nil {
		return policyauthority.ErrIdentity
	}
	return nil
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
	fields, err := panelruntime.DecodeNodeAuthorityObject(bytes.NewReader(raw), "schema", "identity", "sourceId", "snapshotDigest", "phase", "executionRole")
	if err != nil {
		return manifest, ErrAuthorityNotInitialized
	}
	if _, err := panelruntime.DecodeNodeAuthorityObject(bytes.NewReader(fields["identity"]), "authorityId", "generation"); err != nil {
		return manifest, ErrAuthorityNotInitialized
	}
	if role := fields["executionRole"]; role != nil {
		if _, err := panelruntime.DecodeNodeAuthorityObject(bytes.NewReader(role), "mode", "authorityId", "generation", "nodeId"); err != nil {
			return manifest, ErrAuthorityNotInitialized
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, ErrAuthorityNotInitialized
	}
	if decoder.Decode(new(any)) != io.EOF || manifest.Schema != 1 && manifest.Schema != 2 || manifest.Schema == 1 && manifest.ExecutionRole != nil || manifest.Schema == 2 && manifest.ExecutionRole == nil || manifest.role().Validate() != nil || manifest.Identity.Generation != 1 || len(manifest.Identity.AuthorityID) != 32 || !validPolicySourceKey(manifest.SourceID) || len(manifest.SnapshotDigest) != 64 || manifest.Phase != "preparing" && manifest.Phase != "committed" {
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
