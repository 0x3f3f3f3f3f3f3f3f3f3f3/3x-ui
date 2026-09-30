package xray

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestPasswordProxyHotAddPreservesListenerLevel(t *testing.T) {
	for _, protocol := range []string{"mixed", "http"} {
		t.Run(protocol, func(t *testing.T) {
			api, probe := managedMutationAPI(t, passwordProxyCapabilities("trusted-socks-client-id-v1", "trusted-http-client-id-v1"), false)
			for _, level := range []any{uint32(7), int(7), float64(7), json.Number("7")} {
				user := map[string]any{"user": "wire-user", "pass": "resource", "email": "canonical-owner", "clientId": "A", "level": level}
				if err := api.AddUser(protocol, "password", user); err != nil {
					t.Fatal(err)
				}
				got := <-probe.users
				if got.Level != 7 || got.ClientId != "A" || got.Email != "canonical-owner" {
					t.Fatalf("hot credential lost listener level or canonical identity: level=%d", got.Level)
				}
			}
		})
	}
}

func TestPasswordProxyHotAddRejectsInvalidLevelBeforeMutation(t *testing.T) {
	for _, protocol := range []string{"mixed", "http"} {
		for _, invalid := range []any{-1, 1.5, true, "7", uint64(1 << 32), json.Number("1e100")} {
			t.Run(fmt.Sprintf("%s/%v", protocol, invalid), func(t *testing.T) {
				api, probe := managedMutationAPI(t, passwordProxyCapabilities("trusted-socks-client-id-v1", "trusted-http-client-id-v1"), false)
				user := map[string]any{"user": "wire-user", "pass": "resource", "email": "canonical-owner", "clientId": "A", "level": invalid}
				if err := api.AddUser(protocol, "password", user); err == nil || probe.calls.Load() != 0 {
					t.Fatal("invalid level reached a handler mutation")
				}
			})
		}
	}
}
