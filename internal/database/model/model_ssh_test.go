package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSSHCredentialRecordMergePreservesOmittedAndRedactsConflicts(t *testing.T) {
	stored := ClientRecord{Email: "owner", Password: "unrelated", SSHUsername: "old-user", SSHAuthorizedKeys: "old-key", SSHPassword: "old-secret", UpdatedAt: 1}
	MergeClientRecord(&stored, &ClientRecord{Email: "owner", UpdatedAt: 2})
	if stored.SSHPassword != "old-secret" {
		t.Fatal("omitted merge cleared native authentication")
	}
	conflicts := MergeClientRecord(&stored, &ClientRecord{Email: "owner", SSHUsername: "new-user", SSHAuthorizedKeys: "new-key", SSHPassword: "new-secret", UpdatedAt: 3})
	if stored.SSHUsername != "new-user" || stored.SSHAuthorizedKeys != "new-key" || stored.SSHPassword != "new-secret" || stored.Password != "unrelated" {
		t.Fatal("canonical import dropped SSH rotation or changed another credential")
	}
	raw, err := json.Marshal(conflicts)
	if err != nil || strings.Contains(string(raw), "old-secret") || strings.Contains(string(raw), "new-secret") {
		t.Fatal("SSH merge conflict exposed authentication")
	}
}

func TestSSHHostKeyPrivateMaterialIsExcludedFromOrdinaryJSON(t *testing.T) {
	raw, err := json.Marshal(NativeSSHHostKey{ID: "business", PrivateKeyPEM: "private-business-material", PublicKey: "public", Fingerprint: "pin"})
	if err != nil || strings.Contains(string(raw), "private-business-material") {
		t.Fatal("ordinary host-key projection exposes private material")
	}
}
