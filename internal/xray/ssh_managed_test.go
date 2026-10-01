package xray

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	coressh "github.com/xtls/xray-core/proxy/ssh"
)

func TestSSHManagedAccountRequiresTypedNativeAuthentication(t *testing.T) {
	account, err := buildUserAccount("ssh", map[string]any{"username": "business-user", "password": "business-password", "publicKeys": []string{}})
	if err != nil || account == nil {
		t.Fatalf("native SSH handler account: %v", err)
	}
	instance, err := account.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := instance.(*coressh.Account)
	if !ok || actual.Username != "business-user" || actual.Password != "business-password" {
		t.Fatal("handler projection lost native SSH authentication")
	}
	if _, err := buildUserAccount("ssh", map[string]any{"username": 123, "password": "secret"}); err == nil {
		t.Fatal("untyped SSH username accepted")
	}
}

func TestSSHHandlerNegotiatesNativeCapabilityBeforeMutation(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			names := []string{}
			if supported {
				names = append(names, "trusted-ssh-client-id-v1")
			}
			api, probe := managedMutationAPI(t, passwordProxyCapabilities(names...), false)
			err := api.AddUser("ssh", "native", map[string]any{"username": "wire", "password": "secret", "publicKeys": []string{}, "email": "label", "clientId": "owner"})
			if !supported {
				if !errors.Is(err, ErrClientPolicyCapability) || probe.calls.Load() != 0 {
					t.Fatal("unsupported SSH mutation crossed capability fence")
				}
				return
			}
			if err != nil || probe.calls.Load() != 1 {
				t.Fatalf("supported SSH mutation: %v", err)
			}
			added := <-probe.users
			if added.ClientId != "owner" || added.Email != "label" {
				t.Fatal("SSH handler lost stable owner")
			}
		})
	}
}

func TestSSHEmptyListenerAndRemovalRequireNativeCapability(t *testing.T) {
	api, probe := managedMutationAPI(t, passwordProxyCapabilities(), false)
	err := api.AddInbound([]byte(`{"protocol":"ssh","tag":"native","listen":"127.0.0.1","port":24577,"settings":{"hostKeyFile":"/unused-business-key","users":[]}}`))
	if !errors.Is(err, ErrClientPolicyCapability) || probe.calls.Load() != 0 {
		t.Fatal("empty SSH listener bypassed capability negotiation")
	}
	names, err := ManagedHotDiffCapabilities(&HotDiff{RemovedUsers: []UserOp{{Protocol: "ssh", Tag: "native", Email: "label"}}})
	if err != nil || !slices.Contains(names, "trusted-ssh-client-id-v1") {
		t.Fatalf("SSH removal omitted native capability: %v %v", names, err)
	}
	if err := api.requireManagedControl(context.Background(), names); !errors.Is(err, ErrClientPolicyCapability) {
		t.Fatal("SSH removal accepted preceding core")
	}
}

func TestSSHHotCredentialRotationPreservesListenerAndSiblings(t *testing.T) {
	old, next := makeHotConfig(), makeHotConfig()
	old.InboundConfigs[1].Protocol, next.InboundConfigs[1].Protocol = "ssh", "ssh"
	old.InboundConfigs[1].Settings = []byte(`{"hostKeyFile":"/business/key","allowPassword":true,"users":[{"username":"first","password":"old","publicKeys":[],"email":"A","clientId":"stable-A"},{"username":"second","password":"kept","publicKeys":[],"email":"B","clientId":"stable-B"}]}`)
	next.InboundConfigs[1].Settings = []byte(`{"hostKeyFile":"/business/key","allowPassword":true,"users":[{"username":"first","password":"new","publicKeys":[],"email":"A","clientId":"stable-A"},{"username":"second","password":"kept","publicKeys":[],"email":"B","clientId":"stable-B"}]}`)
	diff, ok := ComputeManagedHotDiff(old, next)
	if !ok || len(diff.RemovedInboundTags) != 0 || len(diff.AddedInbounds) != 0 || len(diff.RemovedUsers) != 1 || len(diff.AddedUsers) != 1 {
		t.Fatalf("SSH rotation replaced listener or sibling: %+v applicable=%t", diff, ok)
	}
	if diff.RemovedUsers[0].Email != "A" || diff.AddedUsers[0].User["clientId"] != "stable-A" {
		t.Fatal("SSH rotation changed wrong owner")
	}
}

func TestSSHEmptyListenerRemovalStillRequiresNativeCapability(t *testing.T) {
	old, next := makeHotConfig(), makeHotConfig()
	old.InboundConfigs[1].Protocol = "ssh"
	old.InboundConfigs[1].Settings = []byte(`{"hostKeyFile":"/business/key","users":[]}`)
	next.InboundConfigs = next.InboundConfigs[:1]
	diff, ok := ComputeManagedHotDiff(old, next)
	if !ok {
		t.Fatal("empty SSH listener removal cannot be applied")
	}
	names, err := ManagedHotDiffCapabilities(diff)
	if err != nil || !slices.Contains(names, "trusted-ssh-client-id-v1") {
		t.Fatal("empty SSH listener removal crossed native capability fence")
	}
}
