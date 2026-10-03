package policyauthority

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRetainedResetFixturesUsePrivateTemporaryRoots(t *testing.T) {
	for _, tc := range []struct {
		name, environment string
		retain            func(*testing.T, string, string) string
	}{
		{"capture", "RESET_OPERATION_FIXTURE_DIR", retainedResetFixture},
		{"progress", "RESET_OPERATION_PROGRESS_FIXTURE_DIR", retainedResetProgressFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.environment, "")
			original := []byte("original private fixture bytes")
			input := filepath.Join(t.TempDir(), "original.db")
			if err := os.WriteFile(input, original, 0600); err != nil {
				t.Fatal(err)
			}
			copy := tc.retain(t, input, "ordinary-test")
			for _, path := range []string{copy, filepath.Dir(copy), filepath.Dir(filepath.Dir(copy))} {
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
					t.Fatalf("temporary fixture path is not private: %s, %v", path, err)
				}
			}
			data, err := os.ReadFile(copy)
			if err != nil || !bytes.Equal(data, original) {
				t.Fatalf("temporary fixture copy changed bytes: %v", err)
			}
		})
	}
}
