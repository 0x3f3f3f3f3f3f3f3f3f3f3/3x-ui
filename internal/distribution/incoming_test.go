package distribution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestIncomingRejectsSameSizeGeodataTampering(t *testing.T) {
	root := t.TempDir()
	correctionPackage(t, root, strings.Repeat("a", 40), map[string]string{"bin/geoip.dat": "distributed geodata"})
	if err := os.WriteFile(filepath.Join(root, "bin/geoip.dat"), []byte("changed geo bytes!!"), 0600); err != nil {
		t.Fatal(err)
	}
	if len("distributed geodata") != len("changed geo bytes!!") {
		t.Fatal("same-size tampering fixture changed size")
	}
	if _, err := Verify(context.Background(), root); err != nil {
		t.Fatal("installed geodata should remain mutable", err)
	}
	if _, err := VerifyIncoming(context.Background(), root); err == nil {
		t.Fatal("incoming verifier accepted changed geodata checksum")
	}
}
