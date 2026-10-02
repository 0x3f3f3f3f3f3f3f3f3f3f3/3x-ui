package distribution

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
)

// Installed trees may contain business state preserved during promotion.
// Incoming packages must contain only hashed distribution files: especially
// their installer, menu and service scripts cannot be undeclared additions.
func checkIncomingFiles(root string, m *Manifest) error {
	declared := map[string]bool{ManifestName: true}
	for _, f := range m.Files {
		declared[f.Path] = true
	}
	return filepath.WalkDir(root, func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !declared[rel] {
			return fmt.Errorf("incoming package contains an undeclared file: %s", rel)
		}
		return nil
	})
}

func VerifyIncoming(ctx context.Context, root string) (*Manifest, error) {
	m, err := Verify(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := checkIncomingFiles(root, m); err != nil {
		return nil, err
	}
	return m, nil
}
