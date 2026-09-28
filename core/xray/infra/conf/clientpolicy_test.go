package conf_test

import (
	"testing"

	"github.com/xtls/xray-core/infra/conf"
)

func TestManagedClientPolicyRequiresExplicitPersistentStore(t *testing.T) {
	for _, c := range []*conf.ClientPolicyConfig{
		{},
		{StateFile: "/private/policy.db"},
		{InstanceID: "node-1"},
	} {
		if _, err := c.Build(); err == nil {
			t.Fatalf("accepted missing durable state identity: %+v", c)
		}
	}
	c := &conf.ClientPolicyConfig{StateFile: "/private/policy.db", InstanceID: "node-1"}
	if _, err := c.Build(); err != nil {
		t.Fatal(err)
	}
}
