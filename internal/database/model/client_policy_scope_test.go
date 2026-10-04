package model

import (
	"encoding/json"
	"testing"
)

func TestClientPolicyScopePreservesLegacyAndClone(t *testing.T) {
	var absent *ClientPolicyOptions
	if absent.EffectiveScope() != ClientPolicyScopeNode || absent.Validate() != nil {
		t.Fatal("absent legacy policy changed node semantics")
	}
	legacy := &ClientPolicyOptions{Multiplier: "2"}
	if legacy.EffectiveScope() != ClientPolicyScopeNode || legacy.Validate() != nil {
		t.Fatal("legacy three-field policy lost node semantics")
	}
	raw, err := json.Marshal(legacy)
	if err != nil || string(raw) != `{"uploadBytesPerSecond":0,"downloadBytesPerSecond":0,"multiplier":"2"}` {
		t.Fatalf("legacy JSON was silently expanded: %s/%v", raw, err)
	}
	global := ClientPolicyScopeGlobal
	policy := &ClientPolicyOptions{Multiplier: "2", Scope: &global}
	if policy.EffectiveScope() != ClientPolicyScopeGlobal || policy.Validate() != nil {
		t.Fatal("explicit global scope is unavailable")
	}
	cloned := policy.Clone()
	if cloned == nil || cloned.Scope == policy.Scope || cloned.EffectiveScope() != ClientPolicyScopeGlobal {
		t.Fatal("scope pointer was shared with the caller")
	}
	*cloned.Scope = ClientPolicyScopeNode
	if policy.EffectiveScope() != ClientPolicyScopeGlobal {
		t.Fatal("caller mutation retargeted the original policy")
	}
	for _, value := range []ClientPolicyScope{"", "LOCAL", "all", "global "} {
		invalid := &ClientPolicyOptions{Scope: &value}
		if invalid.Validate() == nil {
			t.Fatalf("invalid explicit scope accepted: %q", value)
		}
	}
}
