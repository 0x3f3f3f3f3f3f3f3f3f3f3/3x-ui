package xray

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ValidateConfig uses the installed core without starting listeners. Core
// diagnostics are withheld because configuration errors may contain secrets.
func ValidateConfig(cfg *Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("xray configuration validation encoding failed: %w", err)
	}
	file, err := os.CreateTemp(filepath.Dir(GetConfigPath()), ".validate-*.json")
	if err != nil {
		return fmt.Errorf("xray configuration validation file failed: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("xray configuration validation write failed: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("xray configuration validation close failed: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, GetBinaryPath(), "run", "-test", "-c", file.Name())
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xray configuration validation failed: %w", err)
	}
	return nil
}
