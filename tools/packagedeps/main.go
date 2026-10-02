// packagedeps bundles exact module source archives identified by the compiled
// pair. Local managed sources are supplied by corresponding-source.tar.gz.
package main

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"time"
)

type dependency struct {
	Path          string      `json:"path"`
	Version       string      `json:"version,omitempty"`
	Sum           string      `json:"sum,omitempty"`
	Replacement   *dependency `json:"replacement,omitempty"`
	Archive       string      `json:"archive,omitempty"`
	ArchiveSHA256 string      `json:"archiveSHA256,omitempty"`
	ManagedSource bool        `json:"managedSource,omitempty"`
}

func moduleDependency(m *debug.Module) dependency {
	d := dependency{Path: m.Path, Version: m.Version, Sum: m.Sum}
	if m.Replace != nil {
		r := moduleDependency(m.Replace)
		d.Replacement = &r
	}
	return d
}

func run(root, panel, core string) error {
	if _, err := os.Lstat(filepath.Join(root, "go-modules.json")); !os.IsNotExist(err) {
		return errors.New("refusing to replace an existing dependency manifest")
	}
	if err := os.Mkdir(filepath.Join(root, "dependencies"), 0755); err != nil {
		return err
	}
	modules := make(map[string]dependency)
	for _, binary := range []string{panel, core} {
		info, err := buildinfo.ReadFile(binary)
		if err != nil {
			return err
		}
		if info.GoVersion != "go1.27.1" {
			return errors.New("binary uses an unexpected Go toolchain")
		}
		for _, m := range info.Deps {
			d := moduleDependency(m)
			key := d.Path + "@" + d.Version
			if d.Replacement != nil {
				key += "=>" + d.Replacement.Path + "@" + d.Replacement.Version
			}
			if previous, ok := modules[key]; ok && previous.Sum != d.Sum {
				return errors.New("inconsistent compiled module checksum")
			}
			modules[key] = d
		}
	}
	keys := make([]string, 0, len(modules))
	for key := range modules {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]dependency, 0, len(keys))
	for _, key := range keys {
		d := modules[key]
		effective := &d
		if d.Replacement != nil {
			effective = d.Replacement
		}
		if effective.Version == "" {
			effective.ManagedSource = true
			result = append(result, d)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		output, err := exec.CommandContext(ctx, "go", "mod", "download", "-json", effective.Path+"@"+effective.Version).Output()
		cancel()
		if err != nil {
			return fmt.Errorf("obtain locked dependency %s: %w", effective.Path, err)
		}
		var downloaded struct {
			Zip   string
			Sum   string
			Error string
		}
		if err := json.Unmarshal(output, &downloaded); err != nil {
			return err
		}
		if downloaded.Error != "" || downloaded.Zip == "" || downloaded.Sum == "" || downloaded.Sum != effective.Sum {
			return fmt.Errorf("dependency archive differs from compiled checksum: %s", effective.Path)
		}
		info, err := os.Lstat(downloaded.Zip)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 512*1024*1024 {
			return errors.New("invalid dependency source archive")
		}
		f, err := os.Open(downloaded.Zip)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, io.LimitReader(f, info.Size()+1))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || n != info.Size() {
			return errors.New("dependency source archive changed while hashing")
		}
		effective.ArchiveSHA256 = hex.EncodeToString(hash.Sum(nil))
		effective.Archive = "dependencies/" + effective.ArchiveSHA256 + ".zip"
		destination := filepath.Join(root, filepath.FromSlash(effective.Archive))
		if _, err := os.Lstat(destination); os.IsNotExist(err) {
			source, err := os.Open(downloaded.Zip)
			if err != nil {
				return err
			}
			target, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
			if err != nil {
				_ = source.Close()
				return err
			}
			_, copyErr := io.Copy(target, io.LimitReader(source, info.Size()+1))
			sourceErr, targetErr := source.Close(), target.Close()
			if copyErr != nil || sourceErr != nil || targetErr != nil {
				return errors.New("copy dependency source archive")
			}
		} else if err != nil {
			return err
		}
		result = append(result, d)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "go-modules.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: packagedeps LICENSE_DIRECTORY PANEL CORE")
		os.Exit(1)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
