package xray

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestManagedInboundRejectsPublicListenersAndAmbiguousCredentials(t *testing.T) {
	for name, change := range map[string]func(map[string]any, map[string]any){
		"wildcard listener": func(c, _ map[string]any) { c["listen"] = "0.0.0.0" },
		"domain listener":   func(c, _ map[string]any) { c["listen"] = "localhost" },
		"missing listener":  func(c, _ map[string]any) { delete(c, "listen") },
		"empty users":       func(_, s map[string]any) { s["clients"] = []any{} },
		"missing level": func(_, s map[string]any) {
			delete(s["clients"].([]any)[0].(map[string]any), "level")
		},
		"negative level": func(_, s map[string]any) {
			s["clients"].([]any)[0].(map[string]any)["level"] = -1
		},
		"overflow level": func(_, s map[string]any) {
			s["clients"].([]any)[0].(map[string]any)["level"] = uint64(1) << 32
		},
		"empty password": func(_, s map[string]any) {
			s["clients"].([]any)[0].(map[string]any)["password"] = ""
		},
		"empty identity": func(_, s map[string]any) {
			s["clients"].([]any)[0].(map[string]any)["email"] = ""
		},
		"duplicate identity": func(_, s map[string]any) {
			s["clients"] = append(s["clients"].([]any), map[string]any{"email": "private-test", "password": "another-credential", "level": 123})
		},
		"duplicate password": func(_, s map[string]any) {
			s["clients"] = append(s["clients"].([]any), map[string]any{"email": "another-identity", "password": "owned-test-credential", "level": 124})
		},
		"fallback": func(_, s map[string]any) { s["fallbacks"] = []any{map[string]any{"dest": 80}} },
		"invalid mode": func(_, s map[string]any) {
			s["managed"] = "true"
		},
	} {
		t.Run(name, func(t *testing.T) {
			settings := map[string]any{"managed": true, "clients": []any{map[string]any{"email": "private-test", "password": "owned-test-credential", "level": 123}}}
			candidate := map[string]any{"tag": "private-test", "protocol": "trojan", "listen": "127.0.0.1", "port": 20000, "settings": settings}
			change(candidate, settings)
			raw, err := json.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			built, err := buildAPIInbound(raw)
			if built != nil || !errors.Is(err, ErrManagedInbound) {
				t.Fatalf("unsafe or ambiguous private bridge was built: usable=%t err=%v", built != nil, err)
			}
		})
	}
}
