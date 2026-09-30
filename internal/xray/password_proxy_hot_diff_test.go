package xray

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func passwordHotConfig(t *testing.T, protocol string, accounts ...map[string]any) *Config {
	t.Helper()
	cfg := makeHotConfig()
	if accounts == nil {
		accounts = []map[string]any{}
	}
	settings := map[string]any{"accounts": accounts, "userLevel": 7}
	if protocol == "mixed" {
		settings["auth"] = "password"
	} else {
		settings["requireAuthentication"] = true
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	cfg.InboundConfigs[1].Protocol = protocol
	cfg.InboundConfigs[1].Settings = raw
	return cfg
}

func TestComputeManagedPasswordHotDiffReorderIsEmpty(t *testing.T) {
	for _, protocol := range []string{"mixed", "http"} {
		a := passwordHotAccount("alice", "resource", "A", "a@example.test")
		b := passwordHotAccount("用户", "resource-B", "B", "b@example.test")
		diff, ok := ComputeManagedHotDiff(passwordHotConfig(t, protocol, a, b), passwordHotConfig(t, protocol, b, a))
		if !ok || !diff.Empty() {
			t.Fatalf("%s account order replaces credentials", protocol)
		}
	}
}

func TestComputeManagedPasswordHotDiffTransfersAndRemoves(t *testing.T) {
	for _, protocol := range []string{"mixed", "http"} {
		a := passwordHotAccount("alice", "resource", "A", "a@example.test")
		alias := passwordHotAccount("ALICE", "alias", "A", "a@example.test")
		b := passwordHotAccount("bob", "resource-B", "B", "b@example.test")
		old := passwordHotConfig(t, protocol, a, alias, b)
		next := passwordHotConfig(t, protocol, alias, b, passwordHotAccount("alice", "new", "B", "b@example.test"))
		diff, ok := ComputeManagedHotDiff(old, next)
		if !ok || len(diff.RemovedInboundTags) != 0 || len(diff.AddedInbounds) != 0 || len(diff.RemovedUsers) != 2 || len(diff.AddedUsers) != 3 {
			t.Fatalf("%s username transfer does not replace both changed groups", protocol)
		}
		if diff.RemovedUsers[0].Email != "a@example.test" || diff.RemovedUsers[1].Email != "b@example.test" {
			t.Fatal("group removal order is not deterministic")
		}
		diff, ok = ComputeManagedHotDiff(old, passwordHotConfig(t, protocol))
		if !ok || len(diff.RemovedInboundTags) != 0 || len(diff.RemovedUsers) != 2 || len(diff.AddedUsers) != 0 || !diff.DropsUsers() {
			t.Fatalf("%s final group removal replaces protected listener", protocol)
		}
	}
}

func TestPasswordProxyHotRemovalRequiresProtocolCapabilities(t *testing.T) {
	for _, protocol := range []string{"mixed", "http"} {
		required, err := ManagedHotDiffCapabilities(&HotDiff{RemovedUsers: []UserOp{{Protocol: protocol, Tag: "password", Email: "a@example.test"}}})
		if err != nil || !slices.Contains(required, "trusted-http-client-id-v1") || protocol == "mixed" && !slices.Contains(required, "trusted-socks-client-id-v1") {
			t.Fatalf("%s removal lacks protocol identity preflight: %v %v", protocol, required, err)
		}
	}
}

func TestComputeManagedPasswordHotDiffRejectsAmbiguousOwners(t *testing.T) {
	for _, protocol := range []string{"mixed", "http"} {
		for _, accounts := range [][]map[string]any{
			{passwordHotAccount("alice", "new", "", "a@example.test")},
			{passwordHotAccount("alice", "new", "A", "")},
			{passwordHotAccount("alice", "new", "A", "a@example.test"), passwordHotAccount("alice", "other", "B", "b@example.test")},
			{passwordHotAccount("alice", "new", "A", "a@example.test"), passwordHotAccount("ALICE", "other", "B", "a@example.test")},
			{passwordHotAccount("alice", "new", "A", "a@example.test"), passwordHotAccount("ALICE", "other", "A", "b@example.test")},
		} {
			old := passwordHotConfig(t, protocol, passwordHotAccount("alice", "old", "A", "a@example.test"))
			if _, ok := ComputeManagedHotDiff(old, passwordHotConfig(t, protocol, accounts...)); ok {
				t.Fatalf("%s ambiguous/unowned credential selected hot writes", protocol)
			}
		}
	}
}

func TestComputeManagedPasswordHotDiffPreservesLegacyPath(t *testing.T) {
	old := passwordHotConfig(t, "mixed", passwordHotAccount("alice", "old", "A", "a@example.test"))
	next := passwordHotConfig(t, "mixed", passwordHotAccount("alice", "new", "A", "a@example.test"))
	legacy, ok := ComputeHotDiff(old, next)
	if !ok || len(legacy.RemovedInboundTags) != 1 || len(legacy.AddedInbounds) != 1 || len(legacy.RemovedUsers) != 0 {
		t.Fatal("legacy Mixed diff behavior changed")
	}
	old.InboundConfigs[1].Protocol, next.InboundConfigs[1].Protocol = "socks", "socks"
	if _, ok := ComputeHotDiff(old, next); ok {
		t.Fatal("legacy SOCKS restart protection changed")
	}
	if _, ok := ComputeManagedHotDiff(old, next); ok {
		t.Fatal("managed entry point bypassed legacy SOCKS restart protection")
	}
}

func passwordHotAccount(user, password, id, email string) map[string]any {
	return map[string]any{"user": user, "pass": password, "clientId": id, "email": email}
}

func TestComputeManagedPasswordHotDiffGroupsOwnerAliases(t *testing.T) {
	for _, protocol := range []string{"mixed", "http"} {
		t.Run(protocol, func(t *testing.T) {
			old := passwordHotConfig(t, protocol,
				passwordHotAccount("alice", "old", "A", "a@example.test"),
				passwordHotAccount("ALICE", "alias", "A", "a@example.test"),
				passwordHotAccount("用户", "other", "B", "b@example.test"))
			next := passwordHotConfig(t, protocol,
				passwordHotAccount("alice", "new", "A", "a@example.test"),
				passwordHotAccount("ALICE", "alias", "A", "a@example.test"),
				passwordHotAccount("用户", "other", "B", "b@example.test"))
			diff, ok := ComputeManagedHotDiff(old, next)
			if !ok || len(diff.RemovedInboundTags) != 0 || len(diff.AddedInbounds) != 0 {
				t.Fatal("credential edit replaces the shared listener")
			}
			if len(diff.RemovedUsers) != 1 || diff.RemovedUsers[0].Email != "a@example.test" || len(diff.AddedUsers) != 2 {
				t.Fatalf("grouped operations: removed=%d added=%d", len(diff.RemovedUsers), len(diff.AddedUsers))
			}
			seen := map[string]string{}
			for _, op := range diff.AddedUsers {
				if op.Tag != old.InboundConfigs[1].Tag || op.Protocol != protocol || op.Email != "a@example.test" || op.User["clientId"] != "A" {
					t.Fatal("credential edit touches another owner or loses identity")
				}
				user, _ := op.User["user"].(string)
				password, _ := op.User["pass"].(string)
				seen[user] = password
			}
			if !reflect.DeepEqual(seen, map[string]string{"alice": "new", "ALICE": "alias"}) {
				t.Fatal("changed group did not re-add its unchanged exact-case alias")
			}
		})
	}
}
