package model

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSSHClientRecordRoundTripAndMerge(t *testing.T) {
	client := Client{Email: "ssh-user", SSH: &SSHClient{PublicKeys: []string{"public-only-fixture"}, Targets: []SSHTarget{{Host: "example.invalid", Port: 443}}, Reverse: []SSHRemoteBind{{Address: "::1", Port: 32001}}}}
	record := client.ToRecord()
	record.UpdatedAt = 100
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"ssh":{"publicKeys"`) {
		t.Fatal("SSH JSON was encoded as a string")
	}
	var decoded ClientRecord
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.ToClient().SSH, client.SSH) {
		t.Fatal("SSH keys or permissions were lost in the client API round trip")
	}
	MergeClientRecord(record, &ClientRecord{Email: client.Email, UpdatedAt: 200})
	if !reflect.DeepEqual(record.ToClient().SSH, client.SSH) {
		t.Fatal("a snapshot without SSH fields erased credentials")
	}
	previous := record.SSHConfig
	newer := (&Client{Email: client.Email, SSH: &SSHClient{PublicKeys: []string{"new-public-only-fixture"}}}).ToRecord()
	newer.UpdatedAt = 300
	MergeClientRecord(record, newer)
	if record.SSHConfig != newer.SSHConfig {
		t.Fatal("newer SSH credential rotation was dropped")
	}
	MergeClientRecord(record, &ClientRecord{Email: client.Email, UpdatedAt: 100, SSHConfig: previous})
	if record.SSHConfig != newer.SSHConfig {
		t.Fatal("stale snapshot revived a revoked SSH credential")
	}
}
