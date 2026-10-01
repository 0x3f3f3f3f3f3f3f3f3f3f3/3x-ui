package sub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/infra/conf"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/testpg"
)

func seedNativeSnellExport(t *testing.T, version int) (*model.Inbound, *model.ClientRecord) {
	t.Helper()
	cleanup, err := testpg.IsolatePackage("snell_export")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	oldFS := distFS
	distFS = testDistFS
	t.Cleanup(func() { distFS = oldFS })
	seedSubDB(t)
	owner := &model.ClientRecord{
		Email: "owner,#\"独立", SubID: "snell-native-sub", Enable: true,
		SnellPSK: "独立#comma,space \"\\native\u200bkey", Password: "never-export-ordinary-password",
		SSHPassword: "never-export-ssh-password", MieruPassword: "never-export-mieru-password",
	}
	if err := database.GetDB().Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	settings := fmt.Sprintf(`{"version":%d,"obfs":"off","quic":%t,"clients":[{"email":"forged","snellPsk":"forged-secret"}]}`, version, version == 5)
	if version == 6 {
		settings = strings.Replace(settings, `"obfs":"off"`, `"obfs":"off","mode":"unshaped"`, 1)
	}
	inbound := &model.Inbound{
		UserId: 1, Protocol: model.Snell, Enable: true, Listen: "2001:db8::1", Port: 8443,
		Tag: "native-snell", Remark: "Snell,#\"原生", Settings: settings, StreamSettings: "{}",
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.ClientInbound{ClientId: owner.Id, InboundId: inbound.Id}).Error; err != nil {
		t.Fatal(err)
	}
	return inbound, owner
}

func nativeSnellExportResponse(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Accept", "text/plain")
	response := httptest.NewRecorder()
	newSubscriptionTestRouter(subscriptionTestRouterConfig{}).ServeHTTP(response, request)
	return response
}

func TestSnellNativeSurgeSubscriptionPreservesCanonicalUTF8PSK(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			_, owner := seedNativeSnellExport(t, version)
			response := nativeSnellExportResponse(t, "/sub/snell-native-sub?format=snell-surge")
			if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "text/plain") {
				t.Fatalf("native Surge response: %d %s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			quoted := `psk="` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(owner.SnellPSK) + `"`
			for _, required := range []string{"[Proxy]", "snell,", "2001:db8::1", "8443", fmt.Sprintf("version=%d", version), quoted} {
				if !strings.Contains(body, required) {
					t.Fatalf("Surge omitted native value %q: %s", required, body)
				}
			}
			for _, forbidden := range []string{"snell://", "quic=", "forged-secret", owner.Password, owner.SSHPassword, owner.MieruPassword, owner.StableID, `\u200b`} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("Surge included inappropriate value %q", forbidden)
				}
			}
			if version == 6 && !strings.Contains(body, "mode=unshaped") {
				t.Fatal("v6 native shaping mode lost")
			}
		})
	}
}

func TestSnellNativeJSONSubscriptionBuildsActualCoreConfiguration(t *testing.T) {
	for _, version := range []int{4, 5, 6} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			_, owner := seedNativeSnellExport(t, version)
			response := nativeSnellExportResponse(t, "/sub/snell-native-sub?format=snell-json")
			if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("native JSON response: %d %s", response.Code, response.Body.String())
			}
			var config conf.Config
			if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Build(); err != nil {
				t.Fatalf("export did not build with actual native core: %v", err)
			}
			var wire struct {
				Outbounds []struct {
					Protocol string
					Settings map[string]any
				} `json:"outbounds"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil || len(wire.Outbounds) == 0 {
				t.Fatalf("missing native outbound: %v", err)
			}
			outbound := wire.Outbounds[0]
			if outbound.Protocol != "snell" || outbound.Settings["psk"] != owner.SnellPSK || outbound.Settings["version"] != float64(version) || outbound.Settings["address"] != "2001:db8::1" || outbound.Settings["port"] != float64(8443) {
				t.Fatalf("native JSON lost canonical credential, version or explicit endpoint: %+v", outbound)
			}
			for _, forbidden := range []string{"forged-secret", owner.Password, owner.SSHPassword, owner.MieruPassword, owner.StableID, `"clientId"`, `"mux"`} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("native JSON included inappropriate value %q", forbidden)
				}
			}
		})
	}
}

func TestSnellNativeSurgeRejectsControlPSKButJSONPreservesIt(t *testing.T) {
	assertNativeSnellControlExport(t, "native\x01line\n\r\tcontrol-key")
}

func TestSnellSQLiteNativeJSONPreservesNULPSK(t *testing.T) {
	// PostgreSQL text cannot store NUL. Preserve the SQLite representation test
	// separately from actual PostgreSQL newline/control export acceptance.
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	assertNativeSnellControlExport(t, "native\x00line\n\rcontrol-key")
}

func assertNativeSnellControlExport(t *testing.T, psk string) {
	t.Helper()
	_, owner := seedNativeSnellExport(t, 6)
	if err := database.GetDB().Model(owner).Update("snell_psk", psk).Error; err != nil {
		t.Fatal(err)
	}
	text := nativeSnellExportResponse(t, "/sub/snell-native-sub?format=snell-surge")
	if text.Code == http.StatusOK || !strings.Contains(strings.ToLower(text.Body.String()), "snell-json") {
		t.Fatal("unrepresentable native text did not direct the user to truthful JSON")
	}
	response := nativeSnellExportResponse(t, "/sub/snell-native-sub?format=snell-json")
	if response.Code != http.StatusOK {
		t.Fatalf("native JSON refused representable control bytes: %d", response.Code)
	}
	var wire struct {
		Outbounds []struct {
			Settings struct {
				PSK string `json:"psk"`
			}
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil || len(wire.Outbounds) == 0 || wire.Outbounds[0].Settings.PSK != psk {
		t.Fatal("native JSON changed canonical credential bytes")
	}
}

func TestSnellNativeSubscriptionsRejectUnsupportedFormats(t *testing.T) {
	seedNativeSnellExport(t, 5)
	for _, path := range []string{"/sub/snell-native-sub?format=raw", "/json/snell-native-sub", "/clash/snell-native-sub"} {
		response := nativeSnellExportResponse(t, path)
		if response.Code == http.StatusOK || !strings.Contains(strings.ToLower(response.Body.String()), "snell") {
			t.Fatalf("unsupported format was not explicit: %s -> %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestSnellNativeSubscriptionsExcludeInactiveOwnerOrResource(t *testing.T) {
	for _, state := range []string{"owner-disabled", "expired", "resource-disabled", "excluded"} {
		t.Run(state, func(t *testing.T) {
			inbound, owner := seedNativeSnellExport(t, 6)
			db := database.GetDB()
			var err error
			switch state {
			case "owner-disabled":
				err = db.Model(owner).Update("enable", false).Error
			case "expired":
				err = db.Model(owner).Update("expiry_time", time.Now().Add(-time.Minute).UnixMilli()).Error
			case "resource-disabled":
				err = db.Model(inbound).Update("enable", false).Error
			case "excluded":
				err = db.Model(inbound).Update("exclude_from_sub", true).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, format := range []string{"snell-surge", "snell-json"} {
				response := nativeSnellExportResponse(t, "/sub/snell-native-sub?format="+format)
				if response.Code == http.StatusOK || strings.Contains(response.Body.String(), owner.SnellPSK) {
					t.Fatalf("inactive native export: %s %d", format, response.Code)
				}
			}
		})
	}
}

func TestSnellNativeExportPreservesIDNIPv6AndRepeatedHostLabels(t *testing.T) {
	inbound, _ := seedNativeSnellExport(t, 6)
	for n, address := range []string{"2001:db8::2", "例子.测试", "例子.测试"} {
		host := &model.Host{InboundId: inbound.Id, SortOrder: n, Remark: `same,#"label`, Address: address, Port: 9443, Security: "none"}
		if err := database.GetDB().Create(host).Error; err != nil {
			t.Fatal(err)
		}
	}
	response := nativeSnellExportResponse(t, "/sub/snell-native-sub?format=snell-json")
	if response.Code != http.StatusOK {
		t.Fatalf("mapped native JSON: %d %s", response.Code, response.Body.String())
	}
	var wire struct {
		Outbounds []struct {
			Tag      string
			Settings struct {
				Address string
				Port    int
			}
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil || len(wire.Outbounds) != 3 {
		t.Fatalf("mapped endpoints lost: %v", err)
	}
	tags := map[string]bool{}
	for n, outbound := range wire.Outbounds {
		want := "xn--fsqu00a.xn--0zwm56d"
		if n == 0 {
			want = "2001:db8::2"
		}
		if outbound.Settings.Address != want || outbound.Settings.Port != 9443 || tags[outbound.Tag] {
			t.Fatal("mapped endpoint, IDN or unique native tag changed")
		}
		tags[outbound.Tag] = true
	}
	text := nativeSnellExportResponse(t, "/sub/snell-native-sub?format=snell-surge")
	if text.Code != http.StatusOK || strings.Count(text.Body.String(), " = snell,") != 3 || !strings.Contains(text.Body.String(), "xn--fsqu00a.xn--0zwm56d") {
		t.Fatalf("mapped Surge policies lost: %d %s", text.Code, text.Body.String())
	}
}

func TestSnellNativeExportDoesNotReplaceExcludedHostsWithListenerAddress(t *testing.T) {
	inbound, owner := seedNativeSnellExport(t, 5)
	host := model.Host{InboundId: inbound.Id, Address: "allowed.example", Port: 9443, ExcludeFromSubTypes: []string{"raw"}}
	if err := database.GetDB().Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"snell-surge", "snell-json"} {
		response := nativeSnellExportResponse(t, "/sub/snell-native-sub?format="+format)
		if response.Code == http.StatusOK || strings.Contains(response.Body.String(), owner.SnellPSK) {
			t.Fatalf("excluded host silently published listener endpoint: %s %d %s", format, response.Code, response.Body.String())
		}
	}
}

func TestSnellNativeExportRejectsUnsupportedManagedHostOptions(t *testing.T) {
	for _, name := range []string{"tls", "reality", "sni", "mux", "path", "sockopt", "url", "whitespace"} {
		t.Run(name, func(t *testing.T) {
			inbound, _ := seedNativeSnellExport(t, 5)
			host := &model.Host{InboundId: inbound.Id, Remark: "unsupported", Address: "native.example.test", Security: "same"}
			switch name {
			case "tls", "reality":
				host.Security = name
			case "sni":
				host.Sni = "front.example.test"
			case "mux":
				host.MuxParams = `{"enabled":false,"xudpConcurrency":16}`
			case "path":
				host.Path = "/ws"
			case "sockopt":
				host.SockoptParams = `{"dialerProxy":"wrapped"}`
			case "url":
				host.Address = "https://native.example.test"
			case "whitespace":
				host.Address = "native.example.test injected"
			}
			if err := database.GetDB().Create(host).Error; err != nil {
				t.Fatal(err)
			}
			for _, format := range []string{"snell-surge", "snell-json"} {
				response := nativeSnellExportResponse(t, "/sub/snell-native-sub?format="+format)
				if response.Code == http.StatusOK || response.Body.Len() == 0 {
					t.Fatalf("unsupported native endpoint was silently exported: %s %s %d", name, format, response.Code)
				}
			}
		})
	}
}
