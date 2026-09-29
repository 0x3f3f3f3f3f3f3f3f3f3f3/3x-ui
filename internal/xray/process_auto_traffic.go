package xray

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// The version hint only selects configuration; private peer and boot checks authenticate it.
func automaticTrafficControl(config *Config, supportsTrafficControl bool) (*Config, string, error) {
	if !supportsTrafficControl || runtime.GOOS != "linux" || (len(config.TrafficControl) != 0 && string(config.TrafficControl) != "null") {
		return config, "", nil
	}
	dir, err := os.MkdirTemp("", "xui-traffic-")
	if err != nil {
		return nil, "", err
	}
	control, err := json.Marshal(map[string]string{"listen": filepath.Join(dir, "control.sock")})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", err
	}
	copy := *config
	copy.TrafficControl = control
	return &copy, dir, nil
}
