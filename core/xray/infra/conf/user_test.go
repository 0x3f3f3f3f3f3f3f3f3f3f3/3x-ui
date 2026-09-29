package conf_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/infra/conf"
)

func TestAuthenticatedInboundClientIdentityAliases(t *testing.T) {
	for _, name := range []string{"vless", "vmess"} {
		for _, tc := range []struct {
			name    string
			fields  string
			id      string
			invalid bool
		}{
			{name: "legacy-omission"},
			{name: "camel-case", fields: `,"clientId":"stable-owner"`, id: "stable-owner"},
			{name: "protobuf-spelling", fields: `,"client_id":"stable-owner"`, id: "stable-owner"},
			{name: "matching-aliases", fields: `,"clientId":"stable-owner","client_id":"stable-owner"`, id: "stable-owner"},
			{name: "conflicting-aliases", fields: `,"clientId":"another-owner","client_id":"stable-owner"`, invalid: true},
			{name: "invalid-type", fields: `,"clientId":123`, invalid: true},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				var cfg conf.Buildable = new(conf.VLessInboundConfig)
				if name == "vmess" {
					cfg = new(conf.VMessInboundConfig)
				}
				raw := fmt.Sprintf(`{"decryption":"none","clients":[{"id":"936997e1-3b0c-4de9-9eea-047ee5829d3e","email":"display@example.test","level":5%s}]}`, tc.fields)
				if err := json.Unmarshal([]byte(raw), cfg); err != nil {
					t.Fatal(err)
				}
				built, err := cfg.Build()
				if tc.invalid {
					if err == nil {
						t.Fatal("accepted ambiguous or invalid managed identity")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var users []*protocol.User
				if v, ok := built.(interface{ GetUsers() []*protocol.User }); ok {
					users = v.GetUsers()
				} else {
					users = built.(interface{ GetUser() []*protocol.User }).GetUser()
				}
				user, err := users[0].ToMemoryUser()
				if err != nil || user.ClientID != tc.id || user.Email != "display@example.test" || user.Level != 5 {
					t.Fatalf("lost identity or legacy account metadata: %+v, %v", user, err)
				}
			})
		}
	}
}

func TestShadowsocks2022RejectsUnimplementedManagedIdentity(t *testing.T) {
	var cfg conf.ShadowsocksServerConfig
	if err := json.Unmarshal([]byte(`{"method":"2022-blake3-aes-128-gcm","password":"MDEyMzQ1Njc4OWFiY2RlZg==","clients":[{"password":"YWJjZGVmMDEyMzQ1Njc4OQ==","email":"managed","clientId":"owner"}]}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Build(); err == nil {
		t.Fatal("accepted managed identity without a verified account adapter")
	}
	cfg.Clients[0].ClientID = ""
	if _, err := cfg.Build(); err != nil {
		t.Fatalf("legacy unmodified shadowsocks 2022 configuration failed: %v", err)
	}
}
