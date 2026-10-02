package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	root, closure, lock, dist string
	manifest                  manifest
}

func write(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) save(t *testing.T) {
	t.Helper()
	data, err := json.MarshalIndent(f.manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.closure, "frontend-compiled-deps.json"), data)
}
func makeFixture(t *testing.T, declaredLicense string) *fixture {
	t.Helper()
	root, err := os.MkdirTemp("", "paired-frontend-verifier-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("preserved fixture:", root)
	f := &fixture{root: root, closure: filepath.Join(root, "closure"), lock: filepath.Join(root, "frontend/package-lock.json"), dist: filepath.Join(root, "dist")}
	pkg, _ := json.Marshal(map[string]string{"name": "fixture-react", "version": "19.3.0", "license": declaredLicense, "author": "Original fixture author"})
	module := []byte("module.exports = {version:'19.3.0'}")
	license := append([]byte("Copyright (c) Original fixture author\n\n"), mitTerms...)
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	tarball := tar.NewWriter(gz)
	names := []string{"package/package.json", "package/index.js"}
	contents := [][]byte{pkg, module}
	if declaredLicense == "" {
		names = append(names, "package/LICENSE", "package/NOTICE")
		contents = append(contents, license, []byte("Original additional copyright notice\n"))
	}
	for i, name := range names {
		if err := tarball.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(contents[i])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarball.Write(contents[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := compressed.Bytes()
	sum := sha512.Sum512(archive)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	archiveSum := digest(archive)
	lock, _ := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": map[string]lockedPackage{"node_modules/fixture-react": {Version: "19.3.0", Resolved: "https://registry.npmjs.org/fixture-react/-/fixture-react-19.3.0.tgz", Integrity: integrity, License: declaredLicense}}})
	write(t, f.lock, lock)
	write(t, filepath.Join(root, "frontend/src/main.js"), []byte("import React from 'fixture-react'"))
	write(t, filepath.Join(f.dist, "index.html"), []byte("<script src='assets/index.js'></script>"))
	write(t, filepath.Join(f.dist, "assets/index.js"), module)
	f.manifest = manifest{SchemaVersion: 1, NodeVersion: "v26.10.0", LockfileSHA256: digest(lock)}
	add := func(p string, b []byte) {
		write(t, filepath.Join(f.closure, filepath.FromSlash(p)), b)
		f.manifest.Files = append(f.manifest.Files, fileRecord{Path: p, SHA256: digest(b)})
	}
	add("package-lock.json", lock)
	add("sources/"+archiveSum+".tgz", archive)
	dep := dependency{PackagePath: "node_modules/fixture-react", Name: "fixture-react", Version: "19.3.0", Resolved: "https://registry.npmjs.org/fixture-react/-/fixture-react-19.3.0.tgz", Integrity: integrity, Archive: "sources/" + archiveSum + ".tgz", ArchiveSHA256: archiveSum, PackageJSONSHA256: digest(pkg), Inputs: []inputRecord{{fileRecord: fileRecord{Path: "index.js", SHA256: digest(module)}, Kind: "bundled-module"}}, NoticeKind: "published"}
	if declaredLicense == "" {
		for i := 2; i < len(names); i++ {
			p := "notices/" + archiveSum + "/" + strings.TrimPrefix(names[i], "package/")
			add(p, contents[i])
			dep.Notices = append(dep.Notices, noticeRecord{fileRecord: fileRecord{Path: p, SHA256: digest(contents[i])}, ArchivePath: names[i]})
		}
	} else {
		dep.NoticeKind = "declared-license-template"
		dep.LicenseTemplateSource = templateSources[declaredLicense]
		p := "notices/" + archiveSum + "/DECLARED-LICENSE-" + declaredLicense + ".txt"
		notice := declaredNotice(pkg, declaredLicense)
		add(p, notice)
		dep.Notices = []noticeRecord{{fileRecord: fileRecord{Path: p, SHA256: digest(notice)}, ArchivePath: "package/package.json"}}
	}
	f.manifest.Dependencies = []dependency{dep}
	outputs, err := walkFiles(f.dist)
	if err != nil {
		t.Fatal(err)
	}
	for p, h := range outputs {
		f.manifest.Outputs = append(f.manifest.Outputs, fileRecord{Path: p, SHA256: h})
	}
	inputs, err := buildInputHashes(filepath.Dir(f.lock))
	if err != nil {
		t.Fatal(err)
	}
	for p, h := range inputs {
		f.manifest.BuildInputs = append(f.manifest.BuildInputs, fileRecord{Path: p, SHA256: h})
	}
	f.save(t)
	return f
}

func TestLockedFrontendClosure(t *testing.T) {
	for _, declaration := range []string{"", "MIT", "Apache-2.0"} {
		t.Run("valid-"+declaration, func(t *testing.T) {
			f := makeFixture(t, declaration)
			if err := verify(f.closure, f.lock, f.dist); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestFrontendClosureRejectsTampering(t *testing.T) {
	cases := []struct {
		name    string
		change  func(*testing.T, *fixture)
		message string
	}{
		{"changed-asset", func(t *testing.T, f *fixture) { write(t, filepath.Join(f.dist, "assets/index.js"), []byte("changed")) }, "outputs differ"},
		{"changed-first-party-source", func(t *testing.T, f *fixture) {
			write(t, filepath.Join(f.root, "frontend/src/main.js"), []byte("changed"))
		}, "different source inputs"},
		{"missing-notice", func(t *testing.T, f *fixture) {
			if err := os.Remove(filepath.Join(f.closure, f.manifest.Dependencies[0].Notices[0].Path)); err != nil {
				t.Fatal(err)
			}
		}, "file set or hash"},
		{"notice-and-file-hash-rewritten", func(t *testing.T, f *fixture) {
			n := &f.manifest.Dependencies[0].Notices[0]
			data := []byte("MIT")
			write(t, filepath.Join(f.closure, n.Path), data)
			n.SHA256 = digest(data)
			for i := range f.manifest.Files {
				if f.manifest.Files[i].Path == n.Path {
					f.manifest.Files[i].SHA256 = n.SHA256
				}
			}
			f.save(t)
		}, "notice differs"},
		{"published-notice-omitted-from-manifest", func(t *testing.T, f *fixture) {
			f.manifest.Dependencies[0].Notices = f.manifest.Dependencies[0].Notices[:1]
			f.save(t)
		}, "omits published license"},
		{"source-archive-and-sha-rewritten", func(t *testing.T, f *fixture) {
			dep := &f.manifest.Dependencies[0]
			old := dep.Archive
			data, err := os.ReadFile(filepath.Join(f.closure, old))
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, []byte("tampered")...)
			dep.ArchiveSHA256 = digest(data)
			dep.Archive = "sources/" + dep.ArchiveSHA256 + ".tgz"
			if err := os.Remove(filepath.Join(f.closure, old)); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(f.closure, dep.Archive), data)
			for i := range f.manifest.Files {
				if f.manifest.Files[i].Path == old {
					f.manifest.Files[i] = fileRecord{Path: dep.Archive, SHA256: dep.ArchiveSHA256}
				}
			}
			f.save(t)
		}, "original lock integrity"},
		{"unsafe-manifest-path", func(t *testing.T, f *fixture) { f.manifest.Files[0].Path = "../outside"; f.save(t) }, "invalid frontend manifest"},
		{"link-in-closure", func(t *testing.T, f *fixture) {
			n := f.manifest.Dependencies[0].Notices[0].Path
			p := filepath.Join(f.closure, n)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f.lock, p); err != nil {
				t.Fatal(err)
			}
		}, "regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := makeFixture(t, "")
			tc.change(t, f)
			err := verify(f.closure, f.lock, f.dist)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("want rejection %q, got %v", tc.message, err)
			}
		})
	}
}
func TestFrontendClosureRejectsUnknownLicenseFallback(t *testing.T) {
	f := makeFixture(t, "unknown-license")
	if err := verify(f.closure, f.lock, f.dist); err == nil || !strings.Contains(err.Error(), "supported published license declaration") {
		t.Fatalf("unsupported fallback was accepted: %v", err)
	}
}
