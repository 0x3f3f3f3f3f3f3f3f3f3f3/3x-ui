package conf_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xtls/xray-core/app/commander"
	"github.com/xtls/xray-core/app/trafficcontrol"
	"github.com/xtls/xray-core/infra/conf"
)

func decodeTrafficControl(t *testing.T, endpoint string) *conf.Config {
	t.Helper()
	data, err := json.Marshal(map[string]any{"trafficControl": map[string]any{"listen": endpoint}, "api": map[string]any{"tag": "existing-api", "services": []string{"StatsService"}}, "stats": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var config conf.Config
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	return &config
}

func TestTrafficControlConfigKeepsLegacyAPI(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.sock")
	config := decodeTrafficControl(t, path)
	built, err := config.Build()
	if err != nil {
		t.Fatal(err)
	}
	var apis []*commander.Config
	for _, raw := range built.App {
		object, err := raw.GetInstance()
		if err != nil {
			t.Fatal(err)
		}
		if api, ok := object.(*commander.Config); ok {
			apis = append(apis, api)
		}
	}
	if len(apis) != 2 {
		t.Fatalf("expected separate legacy and private APIs, got %d", len(apis))
	}
	if apis[0].Tag != "existing-api" || apis[0].Listen != "" || len(apis[0].Service) != 1 {
		t.Fatalf("legacy API changed: %v", apis[0])
	}
	if apis[1].Listen != path || len(apis[1].Service) != 1 {
		t.Fatalf("private endpoint missing: %v", apis[1])
	}
	object, err := apis[1].Service[0].GetInstance()
	if _, ok := object.(*trafficcontrol.Config); err != nil || !ok {
		t.Fatalf("wrong control service: %T %v", object, err)
	}
	otherPath := filepath.Join(dir, "replacement.sock")
	config.Override(decodeTrafficControl(t, otherPath), "control-override.json")
	built, err = config.Build()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, raw := range built.App {
		object, _ := raw.GetInstance()
		if api, ok := object.(*commander.Config); ok && api.Listen == otherPath {
			found = true
		}
	}
	if !found {
		t.Fatal("control endpoint override was ignored")
	}
}

func TestTrafficControlConfigRejectsPublicEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "127.0.0.1:10085", "@private-control", "relative.sock"} {
		if _, err := decodeTrafficControl(t, endpoint).Build(); err == nil {
			t.Fatalf("public endpoint %q accepted", endpoint)
		}
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTrafficControl(t, filepath.Join(dir, "control.sock")).Build(); err == nil {
		t.Fatal("shared parent accepted")
	}
}

func TestTrafficControlAPIServiceRequiresPrivateEndpoint(t *testing.T) {
	api := &conf.APIConfig{Tag: "control", Listen: "127.0.0.1:10085", Services: []string{"TrafficControlServiceV1"}}
	if _, err := api.Build(); err == nil {
		t.Fatal("traffic control service accepted TCP or was ignored")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	api.Listen = filepath.Join(dir, "control.sock")
	built, err := api.Build()
	if err != nil || len(built.GetService()) != 1 {
		t.Fatalf("private service missing: %v %v", built, err)
	}
}
