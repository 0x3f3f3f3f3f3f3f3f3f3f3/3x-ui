package model

import (
	"encoding/json"
	"testing"
)

func mieruJSONFields(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestMieruCredentialsRoundTripSeparatelyFromOtherProtocols(t *testing.T) {
	const input = `{"email":"display-label","password":"trojan-password","auth":"hysteria-password","mieruUsername":"认证-user","mieruPassword":"mieru-password"}`
	var client Client
	if err := json.Unmarshal([]byte(input), &client); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"request": &client, "record": client.ToRecord(), "projected": client.ToRecord().ToClient()} {
		t.Run(name, func(t *testing.T) {
			fields := mieruJSONFields(t, value)
			if fields["mieruUsername"] != "认证-user" || fields["mieruPassword"] != "mieru-password" {
				t.Fatal("mieru credentials were discarded by client conversion")
			}
			if fields["password"] != "trojan-password" || fields["auth"] != "hysteria-password" {
				t.Fatal("mieru changed an unrelated protocol credential")
			}
		})
	}
	var record ClientRecord
	if err := json.Unmarshal([]byte(input), &record); err != nil {
		t.Fatal(err)
	}
	if fields := mieruJSONFields(t, record.ToClient().ToRecord()); fields["mieruUsername"] != "认证-user" || fields["mieruPassword"] != "mieru-password" {
		t.Fatal("record import lost mieru credentials")
	}
}

func TestMieruCredentialRecordMergePreservesOmittedAndRedactsConflicts(t *testing.T) {
	var stored, incoming ClientRecord
	if err := json.Unmarshal([]byte(`{"email":"owner","password":"other-password","mieruUsername":"first-user","mieruPassword":"first-secret","updatedAt":1}`), &stored); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"email":"owner","comment":"policy edit","updatedAt":2}`), &incoming); err != nil {
		t.Fatal(err)
	}
	MergeClientRecord(&stored, &incoming)
	fields := mieruJSONFields(t, &stored)
	if fields["mieruUsername"] != "first-user" || fields["mieruPassword"] != "first-secret" {
		t.Fatal("omitted update cleared mieru credentials")
	}
	if err := json.Unmarshal([]byte(`{"email":"owner","mieruUsername":"rotated-user","mieruPassword":"rotated-secret","updatedAt":3}`), &incoming); err != nil {
		t.Fatal(err)
	}
	conflicts := MergeClientRecord(&stored, &incoming)
	fields = mieruJSONFields(t, &stored)
	if fields["mieruUsername"] != "rotated-user" || fields["mieruPassword"] != "rotated-secret" || fields["password"] != "other-password" {
		t.Fatal("explicit mieru rotation did not preserve unrelated credentials")
	}
	for _, conflict := range conflicts {
		if conflict.Field == "mieruPassword" && (conflict.Old == "first-secret" || conflict.New == "rotated-secret" || conflict.Kept == "rotated-secret") {
			t.Fatal("merge conflict exposed a mieru password")
		}
	}
}
