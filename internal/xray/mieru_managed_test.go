package xray

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xtls/xray-core/proxy/mieru"
)

func TestMieruManagedAccountRequiresTypedValidCredentials(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		user := map[string]any{"username": "wire-user", "password": "wire-password"}
		if invalid {
			user["username"] = strings.Repeat("界", 22)
		}
		account, err := buildUserAccount("mieru", user)
		if invalid {
			if err == nil {
				t.Fatal("invalid native account accepted")
			}
			continue
		}
		if err != nil || account == nil {
			t.Fatalf("native typed account missing: %v", err)
		}
		instance, err := account.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		actual, ok := instance.(*mieru.Account)
		if !ok || actual.Username != "wire-user" || actual.Password != "wire-password" {
			t.Fatal("handler account changed native authentication")
		}
	}
}

func TestMieruHandlerNegotiatesNativeCapabilityBeforeMutation(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			names := []string{}
			if supported {
				names = append(names, "trusted-mieru-client-id-v1")
			}
			api, probe := managedMutationAPI(t, passwordProxyCapabilities(names...), false)
			user := map[string]any{"username": "wire-user", "password": "wire-password", "email": "label", "clientId": "owner"}
			err := api.AddUser("mieru", "native", user)
			if !supported {
				if !errors.Is(err, ErrClientPolicyCapability) || probe.calls.Load() != 0 {
					t.Fatalf("unsupported native mutation: calls=%d error=%v", probe.calls.Load(), err)
				}
				return
			}
			if err != nil || probe.calls.Load() != 1 {
				t.Fatalf("supported native add: calls=%d error=%v", probe.calls.Load(), err)
			}
			added := <-probe.users
			if added.ClientId != "owner" || added.Email != "label" {
				t.Fatal("native handler mutation lost canonical owner")
			}
		})
	}
}

func TestMieruEmptyListenerAndRemovalRequireNativeCapability(t *testing.T) {
	api, probe := managedMutationAPI(t, passwordProxyCapabilities(), false)
	err := api.AddInbound([]byte(`{"protocol":"mieru","tag":"native","listen":"127.0.0.1","port":24234,"settings":{"transport":"TCP","users":[]}}`))
	if !errors.Is(err, ErrClientPolicyCapability) || probe.calls.Load() != 0 {
		t.Fatalf("empty native listener bypassed capability check: calls=%d error=%v", probe.calls.Load(), err)
	}
	names, err := ManagedHotDiffCapabilities(&HotDiff{RemovedUsers: []UserOp{{Protocol: "mieru", Tag: "native", Email: "label"}}})
	if err != nil || !slices.Contains(names, "trusted-mieru-client-id-v1") {
		t.Fatalf("native removal omitted protocol capability: %v %v", names, err)
	}
	if err := api.requireManagedControl(context.Background(), names); !errors.Is(err, ErrClientPolicyCapability) {
		t.Fatal("native removal accepted predecessor without its protocol capability")
	}
}

func TestMieruHotCredentialRotationPreservesListenerAndSiblings(t *testing.T) {
	old, next := makeHotConfig(), makeHotConfig()
	old.InboundConfigs[1].Protocol, next.InboundConfigs[1].Protocol = "mieru", "mieru"
	old.InboundConfigs[1].Settings = []byte(`{"transport":"UDP","users":[{"username":"first","password":"old","email":"A","clientId":"stable-A"},{"username":"second","password":"kept","email":"B","clientId":"stable-B"}]}`)
	next.InboundConfigs[1].Settings = []byte(`{"transport":"UDP","users":[{"username":"first","password":"new","email":"A","clientId":"stable-A"},{"username":"second","password":"kept","email":"B","clientId":"stable-B"}]}`)
	diff, ok := ComputeManagedHotDiff(old, next)
	if !ok || len(diff.RemovedInboundTags) != 0 || len(diff.AddedInbounds) != 0 || len(diff.RemovedUsers) != 1 || len(diff.AddedUsers) != 1 {
		t.Fatalf("credential rotation replaced listener/sibling instead of old account: %+v applicable=%t", diff, ok)
	}
	if diff.RemovedUsers[0].Email != "A" || diff.AddedUsers[0].User["clientId"] != "stable-A" || diff.AddedUsers[0].User["password"] != "new" {
		t.Fatal("rotation changed canonical owner or wrong account")
	}
}
