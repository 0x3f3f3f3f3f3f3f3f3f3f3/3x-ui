package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratePolicyScopeAndAccountContracts(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	relative, err := filepath.Rel(root, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := run(root, relative); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"types.ts", "zod.ts"} {
		raw, err := os.ReadFile(filepath.Join(out, file))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if strings.Contains(text, "authorityRenewalReadyHeap") || strings.Contains(text, "authorityRenewalOperationOrder") {
			t.Errorf("private scheduler alias produced undefined API references in %s", file)
		}
		if !strings.Contains(text, "ClientPolicyNodeAccount") {
			t.Errorf("canonical account contract missing from %s", file)
		}
	}
	zod, err := os.ReadFile(filepath.Join(out, "zod.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(zod), "scope: z.enum(['node', 'global']).optional()") || !strings.Contains(string(zod), "desiredPolicyVersion: z.string()") {
		t.Fatal("scope enum or decimal-string account version lost during generation")
	}
}
