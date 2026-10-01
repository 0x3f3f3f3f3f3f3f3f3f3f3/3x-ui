package conf_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/xtls/xray-core/infra/conf"
)

// Rejecting/forgetting native Snell registration must stop a real config build.
func TestNativeSnellConfigVersions(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			var config conf.Config
			input := fmt.Sprintf(`{"inbounds":[{"tag":"snell","listen":"127.0.0.1","port":12345,"protocol":"snell","settings":{"version":%d,"psk":"native-snell-test-secret","clientId":"d395b8b1-31ab-47ea-9a67-e46dcc84cd96","email":"owner"}}],"outbounds":[{"protocol":"snell","settings":{"version":%d,"psk":"native-snell-test-secret","address":"127.0.0.1","port":12346}}]}`, version, version)
			if err := json.Unmarshal([]byte(input), &config); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Build(); err != nil {
				t.Fatalf("native Snell version %d rejected: %v", version, err)
			}
		})
	}
}

func TestNativeSnellConfigRejectsUnsupportedModes(t *testing.T) {
	for _, settings := range []string{
		`"version":3`, `"version":7`, `"version":6,"mode":"unsafe-raw"`,
		`"version":4,"mode":"unshaped"`, `"version":4,"quic":true`, `"version":6,"quic":true`,
		`"version":4,"obfs":"tls"`, `"version":6,"obfs":"http"`,
		`"version":4,"clientId":"not-a-uuid"`,
	} {
		t.Run(settings, func(t *testing.T) {
			var config conf.Config
			input := `{"inbounds":[{"listen":"127.0.0.1","port":12345,"protocol":"snell","settings":{"psk":"native-snell-test-secret","email":"owner","clientId":"d395b8b1-31ab-47ea-9a67-e46dcc84cd96",` + settings + `}}]}`
			if err := json.Unmarshal([]byte(input), &config); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Build(); err == nil {
				t.Fatalf("unsupported Snell settings accepted: %s", settings)
			}
		})
	}
}
