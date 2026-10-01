package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSnellCredentialsCanonicalRoundTripAndRedactedMerge(t *testing.T) {
	var client Client
	if err := json.Unmarshal([]byte(`{"email":"snell-owner","snellPsk":"native-key-before","password":"ordinary-password","sshPassword":"ssh-password","mieruPassword":"mieru-password"}`), &client); err != nil {
		t.Fatal(err)
	}
	stored := client.ToRecord()
	wire, err := json.Marshal(stored.ToClient())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["snellPsk"] != "native-key-before" {
		t.Fatal("canonical conversion discarded native Snell PSK")
	}
	var incoming ClientRecord
	if err := json.Unmarshal([]byte(`{"snellPsk":"native-key-after","updated_at":100}`), &incoming); err != nil {
		t.Fatal(err)
	}
	incoming.UpdatedAt = stored.UpdatedAt + 1
	conflicts := MergeClientRecord(stored, &incoming)
	wire, err = json.Marshal(stored.ToClient())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["snellPsk"] != "native-key-after" || stored.Password != "ordinary-password" || stored.SSHPassword != "ssh-password" || stored.MieruPassword != "mieru-password" {
		t.Fatal("native merge conflated protocol credentials")
	}
	audit, err := json.Marshal(conflicts)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) == 0 || strings.Contains(string(audit), "native-key") {
		t.Fatal("Snell merge audit leaked or omitted credential conflict")
	}
	MergeClientRecord(stored, &ClientRecord{UpdatedAt: 200})
	wire, err = json.Marshal(stored.ToClient())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["snellPsk"] != "native-key-after" {
		t.Fatal("omitted merge erased Snell credential")
	}
}
