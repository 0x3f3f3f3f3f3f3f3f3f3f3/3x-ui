package xray

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/xtls/xray-core/proxy/snell"
)

func TestSnellManagedAccountRequiresTypedNativePSK(t *testing.T) {
	account, err := buildUserAccount("snell", map[string]any{"psk": "native-snell-key"})
	if err != nil || account == nil {
		t.Fatalf("native Snell account: %v", err)
	}
	instance, err := account.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := instance.(*snell.Account)
	if !ok || actual.Psk != "native-snell-key" {
		t.Fatal("native account lost PSK")
	}
	for _, value := range []any{123, "", nil} {
		if _, err := buildUserAccount("snell", map[string]any{"psk": value}); err == nil {
			t.Fatal("invalid typed PSK accepted")
		}
	}
}

func TestSnellHandlerNegotiatesNativeCapabilityBeforeMutation(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprint(supported), func(t *testing.T) {
			names := []string{}
			if supported {
				names = append(names, "trusted-snell-client-id-v1")
			}
			api, probe := managedMutationAPI(t, passwordProxyCapabilities(names...), false)
			err := api.AddUser("snell", "native", map[string]any{"psk": "native-snell-key", "email": "label", "clientId": "00000000-0000-4000-8000-000000000001"})
			if !supported {
				if !errors.Is(err, ErrClientPolicyCapability) || probe.calls.Load() != 0 {
					t.Fatal("unsupported Snell mutation crossed capability fence")
				}
				return
			}
			if err != nil || probe.calls.Load() != 1 {
				t.Fatalf("supported native mutation: %v", err)
			}
			added := <-probe.users
			if added.ClientId != "00000000-0000-4000-8000-000000000001" || added.Email != "label" {
				t.Fatal("native account lost SQL owner")
			}
		})
	}
}

func TestSnellHotCredentialRotationPreservesListenerAndSiblings(t *testing.T) {
	old, next := makeHotConfig(), makeHotConfig()
	old.InboundConfigs[1].Protocol, next.InboundConfigs[1].Protocol = "snell", "snell"
	old.InboundConfigs[1].Settings = []byte(`{"version":5,"psk":"native-key-old","clientId":"00000000-0000-4000-8000-000000000001","email":"A","obfs":"off","quic":true}`)
	next.InboundConfigs[1].Settings = []byte(`{"version":5,"psk":"native-key-new","clientId":"00000000-0000-4000-8000-000000000001","email":"A","obfs":"off","quic":true}`)
	diff, ok := ComputeManagedHotDiff(old, next)
	if !ok || len(diff.RemovedInboundTags) != 0 || len(diff.AddedInbounds) != 0 || len(diff.RemovedUsers) != 1 || len(diff.AddedUsers) != 1 {
		t.Fatalf("native rotation replaced listener: %+v applicable=%t", diff, ok)
	}
	if diff.RemovedUsers[0].Email != "A" || diff.AddedUsers[0].User["psk"] != "native-key-new" || diff.AddedUsers[0].User["clientId"] != "00000000-0000-4000-8000-000000000001" {
		t.Fatal("native rotation changed wrong account")
	}
}

func TestSnellListenerAddAndRemovalRequireNativeCapability(t *testing.T) {
	api, probe := managedMutationAPI(t, passwordProxyCapabilities(), false)
	err := api.AddInbound([]byte(`{"protocol":"snell","tag":"native","listen":"127.0.0.1","port":24579,"settings":{"version":5,"psk":"native-key","email":"A","clientId":"00000000-0000-4000-8000-000000000001"}}`))
	if !errors.Is(err, ErrClientPolicyCapability) || probe.calls.Load() != 0 {
		t.Fatal("native listener bypassed capability negotiation")
	}
	for _, diff := range []*HotDiff{
		{RemovedUsers: []UserOp{{Protocol: "snell", Tag: "native", Email: "A"}}},
		{RemovedInboundTags: []string{"native"}, RemovedInboundProtocols: map[string]string{"native": "snell"}},
	} {
		names, err := ManagedHotDiffCapabilities(diff)
		if err != nil || !slices.Contains(names, "trusted-snell-client-id-v1") {
			t.Fatalf("Snell removal omitted native capability: %v %v", names, err)
		}
		if err := api.requireManagedControl(context.Background(), names); !errors.Is(err, ErrClientPolicyCapability) {
			t.Fatal("native removal accepted preceding core")
		}
	}
}
