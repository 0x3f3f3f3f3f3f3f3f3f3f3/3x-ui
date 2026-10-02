package distribution

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIncomingRejectsUnhashedInstallerAndBusinessState(t *testing.T) {
	for _, name := range []string{"install.sh", "update.sh", "x-ui.db", "business.key"} {
		t.Run(name, func(t *testing.T) {
			root, m := manifestFixture(t)
			writeManifestFixture(t, root, m)
			if err := checkIncomingFiles(root, &m); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, name), []byte("undeclared input"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyFiles(root); err != nil {
				t.Fatal("installed-tree verification must allow preserved state", err)
			}
			if err := checkIncomingFiles(root, &m); err == nil {
				t.Fatal("accepted unhashed incoming resource")
			}
		})
	}
}
