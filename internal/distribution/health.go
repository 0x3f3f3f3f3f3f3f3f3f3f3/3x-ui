package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const HealthName = "panel-health.json"

type RuntimeHealth struct {
	FormatVersion    int    `json:"formatVersion"`
	SourceRevision   string `json:"sourceRevision"`
	InstallationRoot string `json:"installationRoot"`
	ObservedAt       int64  `json:"observedAt"`
	Ready            bool   `json:"ready"`
	PanelPID         int    `json:"panelPid"`
	PanelStart       string `json:"panelStart"`
	CorePID          int    `json:"corePid"`
	CoreStart        string `json:"coreStart"`
}

func writeHealth(name string, r *RuntimeHealth) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(name), ".panel-health-*")
	if err != nil {
		return err
	}
	path := temporary.Name()
	_, writeErr := temporary.Write(append(data, '\n'))
	closeErr := temporary.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("cannot write runtime health")
	}
	if err := os.Rename(path, name); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// PublishRuntimeHealth exposes readiness only through a private local file.
// It contains process/source identity, never credentials, database contents or
// policy allocation state. It is refreshed by the actual panel lifecycle.
func PublishRuntimeHealth(directory string, corePID int, ready bool) error {
	if runtime.GOOS != "linux" || !revisionPattern.MatchString(PanelSourceRevision) {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	root := filepath.Dir(exe)
	panelStart, err := processIdentity(os.Getpid(), filepath.Join(root, "x-ui"))
	if err != nil {
		return err
	}
	coreStart := ""
	if corePID > 0 {
		coreStart, err = processIdentity(corePID, filepath.Join(root, "bin", CoreBinaryName(CurrentTarget().OS, CurrentTarget().Arch)))
		if err != nil {
			ready = false
		}
	}
	r := RuntimeHealth{FormatVersion: 1, SourceRevision: PanelSourceRevision, InstallationRoot: root, ObservedAt: time.Now().UnixMilli(), Ready: ready && corePID > 0 && coreStart != "", PanelPID: os.Getpid(), PanelStart: panelStart, CorePID: corePID, CoreStart: coreStart}
	return writeHealth(filepath.Join(directory, HealthName), &r)
}

func checkHealthRecord(ctx context.Context, root, name, source string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if filepath.Base(name) != HealthName {
		return errors.New("invalid runtime health filename")
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() <= 0 || info.Size() > 16384 {
		return errors.New("runtime health must be a bounded private regular file")
	}
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(opened, info) {
		return errors.New("runtime health changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return err
	}
	var r RuntimeHealth
	if err := decodeStrict(data, &r); err != nil {
		return err
	}
	age := time.Now().UnixMilli() - r.ObservedAt
	if r.FormatVersion != 1 || r.SourceRevision != source || r.InstallationRoot != root || !r.Ready || age < -1000 || age > 5000 {
		return errors.New("runtime health is stale, unready or belongs to another installation/source")
	}
	panelStart, err := processIdentity(r.PanelPID, filepath.Join(root, "x-ui"))
	if err != nil || panelStart != r.PanelStart {
		return errors.New("expected panel process is not alive")
	}
	coreStart, err := processIdentity(r.CorePID, filepath.Join(root, "bin", CoreBinaryName(CurrentTarget().OS, CurrentTarget().Arch)))
	if err != nil || coreStart != r.CoreStart {
		return errors.New("expected managed core process is not alive")
	}
	return nil
}

// WaitRuntimeHealth is bounded; it verifies the installed pair first, then
// requires fresh readiness from the exact panel and managed child processes.
func WaitRuntimeHealth(ctx context.Context, root, name string) error {
	if runtime.GOOS != "linux" {
		return errors.New("paired runtime activation checking currently requires Linux")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	m, err := Verify(ctx, root)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var last error
	for {
		last = checkHealthRecord(ctx, root, name, m.SourceRevision)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("managed core activation not ready: %w (last check: %v)", ctx.Err(), last)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
