// frontenddepsverify validates a prebuilt frontend and its offline source and
// notice closure on the build host. It never downloads or executes npm code.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

type fileRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type noticeRecord struct {
	fileRecord
	ArchivePath string `json:"archivePath"`
}
type inputRecord struct {
	fileRecord
	Kind string `json:"kind"`
}
type dependency struct {
	PackagePath           string         `json:"packagePath"`
	Name                  string         `json:"name"`
	Version               string         `json:"version"`
	Resolved              string         `json:"resolved"`
	Integrity             string         `json:"integrity"`
	Archive               string         `json:"archive"`
	ArchiveSHA256         string         `json:"archiveSHA256"`
	PackageJSONSHA256     string         `json:"packageJSONSHA256"`
	Inputs                []inputRecord  `json:"inputs"`
	Notices               []noticeRecord `json:"notices"`
	NoticeKind            string         `json:"noticeKind"`
	LicenseTemplateSource string         `json:"licenseTemplateSource"`
}
type manifest struct {
	SchemaVersion          int                     `json:"schemaVersion"`
	NodeVersion            string                  `json:"nodeVersion"`
	LockfileSHA256         string                  `json:"lockfileSHA256"`
	BuildInputs            []fileRecord            `json:"buildInputs"`
	Outputs                []fileRecord            `json:"outputs"`
	Dependencies           []dependency            `json:"dependencies"`
	Files                  []fileRecord            `json:"files"`
	GeneratedRuntimeInputs []generatedRuntimeInput `json:"generatedRuntimeInputs"`
}
type generatedRuntimeInput struct {
	ID          string `json:"id"`
	PackagePath string `json:"packagePath"`
	SourcePath  string `json:"sourcePath"`
}
type lockedPackage struct {
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
	Link      bool   `json:"link"`
	License   string `json:"license"`
}

//go:embed licenses/MIT.txt
var mitTerms []byte

//go:embed licenses/Apache-2.0.txt
var apacheTerms []byte

var templateSources = map[string]string{"MIT": "https://spdx.org/licenses/MIT.html", "Apache-2.0": "https://www.apache.org/licenses/LICENSE-2.0.txt"}

func declaredNotice(metadata []byte, license string) []byte {
	result := []byte(fmt.Sprintf("No complete standalone license text was published in this locked npm archive.\nAttribution and the %s declaration below are the original package.json;\npublished notices are retained separately. The following terms are the\nstandard %s license. No copyright year or holder has been inferred.\n\n", license, license))
	result = append(result, metadata...)
	result = append(result, []byte(fmt.Sprintf("\n\nStandard %s license terms:\n\n", license))...)
	if license == "MIT" {
		return append(result, mitTerms...)
	}
	if license == "Apache-2.0" {
		return append(result, apacheTerms...)
	}
	return nil
}

var noticeName = regexp.MustCompile(`(?i)^(licen[sc]e|copying|copyright|notice|third[-_ ]?party([-_ ]?(licen[sc]es?|notices?))?)([._ -]|$)`)
var readmeName = regexp.MustCompile(`(?i)^readme(\.|$)`)
var noticeText = regexp.MustCompile(`(?i)permission is hereby granted|redistribution and use|copyright|license|licence`)

func completeTerms(data []byte) bool {
	text := strings.ToLower(string(data))
	return strings.Contains(text, "permission is hereby granted") && strings.Contains(text, "the software is provided") || strings.Contains(text, "terms and conditions for use, reproduction") && strings.Contains(text, "end of terms and conditions")
}

func declaredLicense(metadata []byte) string {
	var pkg struct {
		License  string `json:"license"`
		Licenses []struct {
			Type string `json:"type"`
		} `json:"licenses"`
	}
	if json.Unmarshal(metadata, &pkg) != nil {
		return ""
	}
	if pkg.License != "" {
		return pkg.License
	}
	if len(pkg.Licenses) == 1 {
		return pkg.Licenses[0].Type
	}
	return ""
}

func safePath(p string) bool {
	if p == "" || strings.ContainsAny(p, "\\\x00:") || path.IsAbs(p) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func readFile(p string) ([]byte, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 128*1024*1024 {
		return nil, fmt.Errorf("not a bounded regular file: %s", p)
	}
	return os.ReadFile(p)
}
func walkFiles(root string) (map[string]string, error) {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("frontend/closure root must be a directory without a link")
	}
	files := make(map[string]string)
	var total int64
	err = filepath.WalkDir(root, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !safePath(rel) {
			return errors.New("unsafe frontend/closure path")
		}
		if entry.IsDir() {
			return nil
		}
		data, err := readFile(p)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > 2*1024*1024*1024 || len(files) > 20000 {
			return errors.New("frontend/closure exceeds distribution bounds")
		}
		files[rel] = digest(data)
		return nil
	})
	return files, err
}
func records(files []fileRecord) (map[string]string, error) {
	result := make(map[string]string)
	for _, f := range files {
		if !safePath(f.Path) || len(f.SHA256) != 64 {
			return nil, errors.New("invalid frontend manifest file")
		}
		if _, err := hex.DecodeString(f.SHA256); err != nil {
			return nil, err
		}
		if _, ok := result[f.Path]; ok {
			return nil, errors.New("duplicate frontend manifest file")
		}
		result[f.Path] = f.SHA256
	}
	return result, nil
}

func buildInputHashes(frontend string) (map[string]string, error) {
	repository := filepath.Dir(frontend)
	result := make(map[string]string)
	for _, directory := range []string{"frontend/src", "frontend/public", "frontend/scripts", "internal/web/translation"} {
		root := filepath.Join(repository, filepath.FromSlash(directory))
		if _, err := os.Lstat(root); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		files, err := walkFiles(root)
		if err != nil {
			return nil, err
		}
		for file, sum := range files {
			result[directory+"/"+file] = sum
		}
	}
	for _, file := range []string{".nvmrc", "frontend/package.json", "frontend/package-lock.json", "frontend/vite.config.js", "frontend/index.html", "frontend/login.html", "frontend/subpage.html", "tools/frontend-dependencies.mjs", "tools/frontenddepsverify/licenses/MIT.txt", "tools/frontenddepsverify/licenses/Apache-2.0.txt"} {
		p := filepath.Join(repository, filepath.FromSlash(file))
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		data, err := readFile(p)
		if err != nil {
			return nil, err
		}
		result[file] = digest(data)
	}
	return result, nil
}
func verifySRI(data []byte, integrity string) error {
	parts := strings.Split(integrity, "-")
	if len(parts) != 2 {
		return errors.New("invalid original npm integrity")
	}
	var h hash.Hash
	switch parts[0] {
	case "sha512":
		h = sha512.New()
	case "sha384":
		h = sha512.New384()
	case "sha256":
		h = sha256.New()
	case "sha1":
		h = sha1.New()
	default:
		return errors.New("unsupported original npm integrity")
	}
	wanted, err := base64.StdEncoding.Strict().DecodeString(parts[1])
	if err != nil || len(wanted) != h.Size() {
		return errors.New("invalid original npm integrity digest")
	}
	_, _ = h.Write(data)
	if !bytes.Equal(wanted, h.Sum(nil)) {
		return errors.New("npm source archive differs from original lock integrity")
	}
	return nil
}
func archiveFiles(data []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	reader := tar.NewReader(io.LimitReader(gz, 256*1024*1024+1))
	files := make(map[string][]byte)
	var total int64
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if !safePath(name) || (!strings.HasPrefix(name, "package/") && !(h.Typeflag == tar.TypeDir && name == "package")) {
			return nil, errors.New("unsafe npm source archive path")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return nil, errors.New("npm source archive contains a link or special file")
		}
		total += h.Size
		if h.Size < 0 || total > 256*1024*1024 {
			return nil, errors.New("npm expanded archive exceeds bound")
		}
		if _, ok := files[name]; ok {
			return nil, errors.New("duplicate npm source archive path")
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			return nil, err
		}
		files[name] = body
	}
	return files, nil
}

func verify(closure, lockPath, frontend string) error {
	actualFiles, err := walkFiles(closure)
	if err != nil {
		return err
	}
	data, err := readFile(filepath.Join(closure, "frontend-compiled-deps.json"))
	if err != nil {
		return err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m.SchemaVersion != 1 || m.NodeVersion != "v26.10.0" || len(m.Dependencies) == 0 {
		return errors.New("invalid pinned frontend closure metadata")
	}
	listed, err := records(m.Files)
	if err != nil {
		return err
	}
	delete(actualFiles, "frontend-compiled-deps.json")
	if !reflect.DeepEqual(actualFiles, listed) {
		return errors.New("frontend closure file set or hash differs from manifest")
	}
	lock, err := readFile(lockPath)
	if err != nil {
		return err
	}
	if digest(lock) != m.LockfileSHA256 || listed["package-lock.json"] != m.LockfileSHA256 {
		return errors.New("frontend closure differs from current package-lock")
	}
	var locked struct {
		Version  int                      `json:"lockfileVersion"`
		Packages map[string]lockedPackage `json:"packages"`
	}
	if err := json.Unmarshal(lock, &locked); err != nil {
		return err
	}
	if locked.Version != 3 {
		return errors.New("frontend requires npm lockfile version 3")
	}
	actualInputs, err := buildInputHashes(filepath.Dir(lockPath))
	if err != nil {
		return err
	}
	inputHashes, err := records(m.BuildInputs)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actualInputs, inputHashes) {
		return errors.New("prebuilt frontend was assembled from different source inputs")
	}
	actualOutputs, err := walkFiles(frontend)
	if err != nil {
		return err
	}
	outputHashes, err := records(m.Outputs)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actualOutputs, outputHashes) || outputHashes["index.html"] == "" {
		return errors.New("prebuilt frontend outputs differ from source closure")
	}
	assets := false
	for p := range outputHashes {
		if strings.HasPrefix(p, "assets/") {
			assets = true
		}
	}
	if !assets {
		return errors.New("prebuilt frontend has no assets")
	}
	seen := make(map[string]bool)
	runtimeSources := make(map[string]map[string]bool)
	runtimeIDs := make(map[string]bool)
	for _, r := range m.GeneratedRuntimeInputs {
		wantedPackage, wantedSource := "node_modules/vite", "dist/node/chunks/node.js"
		if strings.Contains(r.ID, "rolldown") {
			wantedPackage, wantedSource = "node_modules/rolldown", "dist/experimental-runtime-base.mjs"
		}
		if (r.ID != "rolldown/runtime.js" && r.ID != "vite/modulepreload-polyfill.js" && r.ID != "vite/preload-helper.js") || runtimeIDs[r.ID] || r.PackagePath != wantedPackage || r.SourcePath != wantedSource {
			return errors.New("unsupported or inconsistent generated frontend runtime")
		}
		runtimeIDs[r.ID] = true
		if runtimeSources[r.PackagePath] == nil {
			runtimeSources[r.PackagePath] = make(map[string]bool)
		}
		runtimeSources[r.PackagePath][r.SourcePath] = true
	}
	expected := map[string]string{"package-lock.json": m.LockfileSHA256}
	for _, dep := range m.Dependencies {
		lp, ok := locked.Packages[dep.PackagePath]
		if !safePath(dep.PackagePath) || !strings.HasPrefix(dep.PackagePath, "node_modules/") || seen[dep.PackagePath] || !ok || lp.Link || lp.Version != dep.Version || lp.Resolved != dep.Resolved || lp.Integrity != dep.Integrity {
			return errors.New("frontend dependency differs from locked package")
		}
		seen[dep.PackagePath] = true
		if dep.Archive != "sources/"+dep.ArchiveSHA256+".tgz" || listed[dep.Archive] != dep.ArchiveSHA256 {
			return errors.New("frontend source archive hash differs")
		}
		archive, err := readFile(filepath.Join(closure, filepath.FromSlash(dep.Archive)))
		if err != nil {
			return err
		}
		if err := verifySRI(archive, lp.Integrity); err != nil {
			return fmt.Errorf("%s: %w", dep.Name, err)
		}
		source, err := archiveFiles(archive)
		if err != nil {
			return err
		}
		metadata := source["package/package.json"]
		var pkg struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			License string `json:"license"`
		}
		if err := json.Unmarshal(metadata, &pkg); err != nil {
			return err
		}
		if pkg.Name != dep.Name || pkg.Version != lp.Version || digest(metadata) != dep.PackageJSONSHA256 {
			return errors.New("frontend archive package metadata differs")
		}
		if len(dep.Inputs) == 0 {
			return errors.New("frontend dependency has no compiled input")
		}
		inputs := make(map[string]bool)
		for _, input := range dep.Inputs {
			original, ok := source["package/"+input.Path]
			if !safePath(input.Path) || inputs[input.Path] || !ok || digest(original) != input.SHA256 || (input.Kind != "bundled-module" && input.Kind != "style-or-asset-input" && input.Kind != "generated-runtime-source") {
				return errors.New("frontend compiled input differs from locked source")
			}
			if input.Kind == "generated-runtime-source" {
				if !runtimeSources[dep.PackagePath][input.Path] {
					return errors.New("frontend generated runtime lacks its source provenance")
				}
				delete(runtimeSources[dep.PackagePath], input.Path)
			}
			inputs[input.Path] = true
		}
		noticePaths := make(map[string]bool)
		for p := range source {
			if noticeName.MatchString(path.Base(p)) {
				noticePaths[p] = true
			}
		}
		if len(noticePaths) == 0 {
			for p, body := range source {
				if readmeName.MatchString(path.Base(p)) && noticeText.Match(body) {
					noticePaths[p] = true
				}
			}
		}
		readmeOnly, complete := true, false
		for p := range noticePaths {
			if !readmeName.MatchString(path.Base(p)) {
				readmeOnly = false
			}
			if completeTerms(source[p]) {
				complete = true
			}
		}
		needsTemplate := len(noticePaths) == 0 || (readmeOnly && !complete)
		kind, wantedNotices := "published", len(noticePaths)
		license := declaredLicense(metadata)
		if needsTemplate {
			kind = "declared-license-template"
			if len(noticePaths) > 0 {
				kind = "published-with-declared-license-template"
			}
			wantedNotices++
			if templateSources[license] == "" || dep.LicenseTemplateSource != templateSources[license] || (lp.License != "" && lp.License != license) {
				return errors.New("frontend dependency omits a supported published license declaration")
			}
		}
		if dep.NoticeKind != kind || len(dep.Notices) != wantedNotices {
			return errors.New("frontend dependency omits published license notices")
		}
		generatedNotice := false
		for _, n := range dep.Notices {
			if needsTemplate && n.ArchivePath == "package/package.json" {
				if generatedNotice || n.Path != "notices/"+dep.ArchiveSHA256+"/DECLARED-LICENSE-"+license+".txt" || digest(declaredNotice(metadata, license)) != n.SHA256 || listed[n.Path] != n.SHA256 {
					return errors.New("frontend declared license differs from locked metadata and standard terms")
				}
				generatedNotice = true
				expected[n.Path] = n.SHA256
				continue
			}
			if !noticePaths[n.ArchivePath] || n.Path != "notices/"+dep.ArchiveSHA256+"/"+strings.TrimPrefix(n.ArchivePath, "package/") || digest(source[n.ArchivePath]) != n.SHA256 || listed[n.Path] != n.SHA256 {
				return errors.New("frontend license notice differs from locked archive")
			}
			delete(noticePaths, n.ArchivePath)
			expected[n.Path] = n.SHA256
		}
		expected[dep.Archive] = dep.ArchiveSHA256
	}
	for _, sources := range runtimeSources {
		if len(sources) != 0 {
			return errors.New("frontend generated runtime source package is absent")
		}
	}
	if !reflect.DeepEqual(expected, listed) {
		return errors.New("frontend closure contains undeclared dependency files")
	}
	return nil
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: frontenddepsverify CLOSURE_DIRECTORY PACKAGE_LOCK_JSON FRONTEND_DIRECTORY")
		os.Exit(1)
	}
	if err := verify(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("frontend locked source, complete notices and compiled asset hashes verified")
}
