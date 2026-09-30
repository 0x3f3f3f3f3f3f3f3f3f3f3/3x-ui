package conf_test

import (
	"encoding/json"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestPasswordIdentityConfigMetadata(t *testing.T) {
	for _, protocol := range []string{"socks", "http"} {
		for _, alias := range []string{"accounts", "users"} {
			t.Run(protocol+"/"+alias, func(t *testing.T) {
				var builder conf.Buildable
				if protocol == "socks" {
					builder = new(conf.SocksServerConfig)
				} else {
					builder = new(conf.HTTPServerConfig)
				}
				raw := `{"auth":"password","` + alias + `":[{"user":"验证-user","pass":"secret","clientId":"stable-owner","email":"canonical"}]}`
				if err := json.Unmarshal([]byte(raw), builder); err != nil {
					t.Fatal(err)
				}
				config, err := builder.Build()
				if err != nil {
					t.Fatal(err)
				}
				checkPasswordIdentityField(t, config, "client_ids", "验证-user", "stable-owner")
				checkPasswordIdentityField(t, config, "account_emails", "验证-user", "canonical")
			})
		}
	}
}

func checkPasswordIdentityField(t *testing.T, config proto.Message, name, user, want string) {
	t.Helper()
	message := config.ProtoReflect()
	field := message.Descriptor().Fields().ByName(protoreflect.Name(name))
	if field == nil || !field.IsMap() || message.Get(field).Map().Get(protoreflect.ValueOfString(user).MapKey()).String() != want {
		t.Fatalf("authenticated account lost %s metadata", name)
	}
}

func TestPasswordIdentityConfigRejectsUntrustedModesAndDuplicates(t *testing.T) {
	for _, raw := range []string{
		`{"auth":"noauth","accounts":[{"user":"user","pass":"secret","clientId":"owner"}]}`,
		`{"accounts":[{"user":"user","pass":"secret","clientId":"owner"}]}`,
		`{"auth":"password","accounts":[{"user":"same","pass":"one","clientId":"owner"},{"user":"same","pass":"two"}]}`,
		`{"auth":"password","accounts":[null]}`,
	} {
		var config conf.SocksServerConfig
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Build(); err == nil {
			t.Fatalf("invalid managed SOCKS identity survived construction: %s", raw)
		}
	}
	for _, raw := range []string{
		`{"accounts":[{"user":"same","pass":"one","clientId":"owner"},{"user":"same","pass":"two"}]}`,
		`{"accounts":[null]}`,
	} {
		var config conf.HTTPServerConfig
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			t.Fatal(err)
		}
		if _, err := config.Build(); err == nil {
			t.Fatalf("invalid managed HTTP identity survived construction: %s", raw)
		}
	}
}

func TestPasswordIdentityLegacyDuplicatePrecedence(t *testing.T) {
	for _, protocol := range []string{"socks", "http"} {
		var builder conf.Buildable
		if protocol == "socks" {
			builder = new(conf.SocksServerConfig)
		} else {
			builder = new(conf.HTTPServerConfig)
		}
		if err := json.Unmarshal([]byte(`{"auth":"password","accounts":[{"user":"same","pass":"one","email":"ignored-legacy-label"},{"user":"same","pass":"two"}]}`), builder); err != nil {
			t.Fatal(err)
		}
		config, err := builder.Build()
		if err != nil {
			t.Fatal(err)
		}
		checkPasswordIdentityField(t, config, "accounts", "same", "two")
		for _, name := range []string{"client_ids", "account_emails"} {
			message := config.ProtoReflect()
			field := message.Descriptor().Fields().ByName(protoreflect.Name(name))
			if field != nil && message.Get(field).Map().Len() != 0 {
				t.Fatalf("legacy %s acquired managed metadata", protocol)
			}
		}
	}
}
