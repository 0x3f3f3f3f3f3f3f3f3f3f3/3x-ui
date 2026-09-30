package xray

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	policycommand "github.com/xtls/xray-core/app/clientpolicy/command"
	corehttp "github.com/xtls/xray-core/proxy/http"
	"github.com/xtls/xray-core/proxy/socks"
)

func passwordProxyCapabilities(names ...string) *policycommand.Capabilities {
	base := []string{"trusted-tunnel-client-id-v1", "authenticated-credential-revocation-v1", "inbound-scoped-session-close-v1", "shared-directional-rate-v1", "fixed-point-billing-v1", "live-session-control-v1", "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1"}
	return &policycommand.Capabilities{ApiVersion: 1, InstanceId: "node", Epoch: 1, Capabilities: append(base, names...)}
}

func TestPasswordProxyHandlerCapabilitiesBeforeMutation(t *testing.T) {
	for _, protocol := range []string{"socks", "mixed", "http"} {
		for _, support := range [][]string{nil, {"trusted-socks-client-id-v1"}, {"trusted-http-client-id-v1"}, {"trusted-socks-client-id-v1", "trusted-http-client-id-v1"}} {
			t.Run(fmt.Sprintf("%s/%v", protocol, support), func(t *testing.T) {
				api, probe := managedMutationAPI(t, passwordProxyCapabilities(support...), false)
				user := map[string]any{"user": "wire-user", "pass": "wire-password", "email": "canonical-account", "clientId": "owner"}
				raw := []byte(fmt.Sprintf(`{"tag":"password","listen":"127.0.0.1","port":24101,"protocol":%q,"settings":{"auth":"password","accounts":[{"user":"wire-user","pass":"wire-password","email":"canonical-account","clientId":"owner"}]}}`, protocol))
				required, err := ManagedHotDiffCapabilities(&HotDiff{AddedInbounds: [][]byte{raw}, AddedUsers: []UserOp{{Protocol: protocol, User: user}}})
				if err != nil || !slices.Contains(required, "trusted-http-client-id-v1") || protocol != "http" && !slices.Contains(required, "trusted-socks-client-id-v1") {
					t.Fatalf("hot preflight lost password-proxy enforcement: %v %v", required, err)
				}
				compatible := slices.Contains(support, "trusted-http-client-id-v1") && (protocol == "http" || slices.Contains(support, "trusted-socks-client-id-v1"))
				for _, mutation := range []func() error{func() error { return api.AddInbound(raw) }, func() error { return api.AddUser(protocol, "password", user) }} {
					err := mutation()
					if compatible && err != nil || !compatible && !errors.Is(err, ErrClientPolicyCapability) {
						t.Fatalf("capability negotiation: compatible=%t err=%v", compatible, err)
					}
				}
				if !compatible {
					if probe.calls.Load() != 0 {
						t.Fatal("unsupported password proxy mutated a handler")
					}
					return
				}
				if probe.calls.Load() != 2 {
					t.Fatalf("compatible handler mutation count: %d", probe.calls.Load())
				}
				got := <-probe.users
				if got.ClientId != "owner" || got.Email != "canonical-account" {
					t.Fatalf("native user request lost identity: %+v", got)
				}
				account, err := got.Account.GetInstance()
				if err != nil {
					t.Fatal(err)
				}
				switch value := account.(type) {
				case *socks.Account:
					if protocol == "http" || value.Username != "wire-user" || value.Password != "wire-password" {
						t.Fatalf("incorrect SOCKS account: %+v", value)
					}
				case *corehttp.Account:
					if protocol != "http" || value.Username != "wire-user" || value.Password != "wire-password" {
						t.Fatalf("incorrect HTTP account: %+v", value)
					}
				default:
					t.Fatalf("unexpected password account type: %T", value)
				}
			})
		}
	}
}

func TestPasswordProxyEmptyAuthenticationRequiresCapability(t *testing.T) {
	for _, protocol := range []string{"socks", "mixed", "http"} {
		for _, supported := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", protocol, supported), func(t *testing.T) {
				caps := passwordProxyCapabilities()
				if supported {
					caps.Capabilities = append(caps.Capabilities, "trusted-http-client-id-v1")
				}
				api, probe := managedMutationAPI(t, caps, false)
				raw := []byte(fmt.Sprintf(`{"tag":"password","listen":"127.0.0.1","port":24101,"protocol":%q,"settings":{"auth":"password","accounts":[],"requireAuthentication":true}}`, protocol))
				err := api.AddInbound(raw)
				if supported {
					if err != nil || probe.calls.Load() != 1 {
						t.Fatalf("capable core rejected empty authenticated listener: %v calls=%d", err, probe.calls.Load())
					}
				} else if !errors.Is(err, ErrClientPolicyCapability) || probe.calls.Load() != 0 {
					t.Fatalf("older core silently accepted anonymous fallback: %v calls=%d", err, probe.calls.Load())
				}
			})
		}
	}
}

func TestPasswordProxyAccountRejectsInvalidCredentialTypes(t *testing.T) {
	api, probe := managedMutationAPI(t, passwordProxyCapabilities("trusted-socks-client-id-v1", "trusted-http-client-id-v1"), false)
	for _, protocol := range []string{"socks", "mixed", "http"} {
		for _, field := range []string{"user", "pass"} {
			for _, invalid := range []any{nil, 123, true, []string{"invalid"}, ""} {
				user := map[string]any{"user": "wire-user", "pass": "wire-password", "email": "canonical-account", "clientId": "owner"}
				user[field] = invalid
				if err := api.AddUser(protocol, "password", user); err == nil {
					t.Fatalf("invalid %s %s credential type %T reached the core", protocol, field, invalid)
				}
			}
		}
	}
	if probe.calls.Load() != 0 {
		t.Fatal("invalid password credential mutated a handler")
	}
}
