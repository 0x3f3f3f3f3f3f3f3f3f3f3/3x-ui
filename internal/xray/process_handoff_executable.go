package xray

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xtls/xray-core/common/platform"
)

// TrafficHandoffExecutable holds a private, verified image until managed startup finishes.
type TrafficHandoffExecutable struct {
	path        string
	environment []string
}

func (e *TrafficHandoffExecutable) Close() error { return os.RemoveAll(filepath.Dir(e.path)) }

func (e *TrafficHandoffExecutable) NewProcess(config *Config) *Process {
	p := NewProcess(config)
	p.binaryPath = e.path
	p.binaryEnv = e.environment
	return p
}

// PinTrafficHandoffExecutable prevents an install-path replacement from changing the next exec.
func (p *Process) PinTrafficHandoffExecutable() (_ *TrafficHandoffExecutable, err error) {
	p.trafficMu.Lock()
	defer p.trafficMu.Unlock()
	if len(p.trafficExecutable) != sha256.Size {
		return nil, ErrTrafficDrainCapability
	}
	source, err := os.Open(GetBinaryPath())
	if err != nil {
		return nil, err
	}
	defer source.Close()
	dir, err := os.MkdirTemp("", "xui-handoff-bin-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "xray")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return nil, err
	}
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, digest), source)
	closeErr := file.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !bytes.Equal(digest.Sum(nil), p.trafficExecutable) {
		return nil, fmt.Errorf("%w: restart image differs from the negotiated child", ErrTrafficDrainCapability)
	}
	environment := os.Environ()
	p.mu.RLock()
	if p.cmd != nil {
		environment = slices.Clone(p.cmd.Env)
	}
	p.mu.RUnlock()
	directory := p.trafficExecutableDir
	if directory == "" {
		directory = filepath.Dir(GetBinaryPath())
	}
	for _, name := range []string{platform.AssetLocation, platform.CertLocation} {
		flag := platform.NewEnvFlag(name)
		if !slices.ContainsFunc(environment, func(value string) bool {
			return strings.HasPrefix(value, flag.Name+"=") || strings.HasPrefix(value, flag.AltName+"=")
		}) {
			environment = append(environment, flag.AltName+"="+directory)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), xrayVersionTimeout)
	defer cancel()
	probe := exec.CommandContext(ctx, path, "-version")
	probe.Env = environment
	if _, err := probe.Output(); err != nil {
		return nil, fmt.Errorf("handoff executable probe: %w", err)
	}
	return &TrafficHandoffExecutable{path: path, environment: environment}, nil
}
