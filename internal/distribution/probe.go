package distribution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// BinaryReport describes compiled features only. It does not assert that a
// running instance is configured, authorized or has a durable accounting store.
type BinaryReport struct {
	FormatVersion  int      `json:"formatVersion"`
	APIVersion     uint32   `json:"apiVersion"`
	Compatibility  string   `json:"compatibility"`
	SourceRevision string   `json:"sourceRevision"`
	Target         Target   `json:"target"`
	GoVersion      string   `json:"goVersion"`
	CoreVersion    string   `json:"coreVersion,omitempty"`
	PanelVersion   string   `json:"panelVersion,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
}

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("candidate output exceeds size limit")
	}
	return b.Buffer.Write(p)
}

func probe(ctx context.Context, executable string, args ...string) (BinaryReport, error) {
	var report BinaryReport
	scratch, err := os.MkdirTemp("", "xui-package-probe-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(scratch)
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = scratch
	cmd.Env = []string{}
	cmd.WaitDelay = 250 * time.Millisecond
	output := &boundedOutput{limit: maxManifestBytes}
	diagnostic := &boundedOutput{limit: 16 * 1024}
	cmd.Stdout, cmd.Stderr = output, diagnostic
	if err := cmd.Run(); err != nil {
		return report, fmt.Errorf("candidate %s probe failed: %w", filepath.Base(executable), err)
	}
	if err := decodeStrict(output.Bytes(), &report); err != nil {
		return report, fmt.Errorf("candidate %s report: %w", filepath.Base(executable), err)
	}
	return report, nil
}

func validateReport(m *Manifest, r BinaryReport, core bool) error {
	if r.FormatVersion != 1 || r.APIVersion != 1 || r.Compatibility != m.Compatibility || r.SourceRevision != m.SourceRevision || r.Target != m.Target || r.GoVersion != m.Toolchains["go"] {
		return errors.New("candidate format, API, compatibility, source or target differs from package")
	}
	if !core {
		return nil
	}
	capabilities := make(map[string]bool)
	for _, name := range r.Capabilities {
		if name == "" || capabilities[name] {
			return errors.New("candidate capabilities are empty or duplicated")
		}
		switch name {
		case "local-durable-reservations-v1", "committed-cumulative-ledger-v1", "create-only-usage-seed-v1", "durable-first-use-expiry-v1":
			return errors.New("offline candidate report claims instance durability")
		}
		capabilities[name] = true
	}
	for _, required := range m.RequiredCapabilities {
		if !capabilities[required] {
			return fmt.Errorf("candidate lacks compiled feature %s", required)
		}
	}
	return nil
}

// Verify validates files and bounded offline reports before any installation or
// service mutation. Each executable must identify the same clean source revision.
func Verify(ctx context.Context, root string) (*Manifest, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	m, err := VerifyFiles(absolute)
	if err != nil {
		return nil, err
	}
	for _, kind := range []string{"panel", "core"} {
		executable, args := filepath.Join(absolute, "x-ui"), []string{"package", "info"}
		if kind == "core" {
			executable, args = filepath.Join(absolute, "bin", CoreBinaryName(m.Target.OS, m.Target.Arch)), []string{"capabilities"}
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		r, err := probe(probeCtx, executable, args...)
		cancel()
		if err != nil {
			return nil, err
		}
		if err := validateReport(m, r, kind == "core"); err != nil {
			return nil, fmt.Errorf("%s: %w", kind, err)
		}
	}
	return m, nil
}
