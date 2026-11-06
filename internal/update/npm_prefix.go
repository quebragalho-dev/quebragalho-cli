package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// npmInstallPrefix infers the npm --prefix owning the invoked installation
// from the wrapper package root findNpmPackageRoot locates. It reports false
// when the root cannot be found or its shape is not a global npm layout, so
// callers fall back to a bare `npm install -g` and let the post-install
// verification reject a success that did not move the invoked install.
func npmInstallPrefix(executablePath string) (string, bool) {
	root, ok := findNpmPackageRoot(executablePath)
	if !ok {
		return "", false
	}
	return npmPrefixFromPackageRoot(root)
}

// findNpmPackageRoot walks up from the executable looking for the wrapper
// package directory: the downloaded binary sits in the wrapper root, while a
// platform-package binary sits a few levels below it. The platform payload
// manifest carries the same package name, so the walk keys on the wrapper
// path shape, not the manifest alone.
func findNpmPackageRoot(executablePath string) (string, bool) {
	dir := filepath.Dir(executablePath)
	for i := 0; i < 12; i++ {
		if isNpmWrapperRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	return "", false
}

// isNpmPackageDir reports whether dir holds an installed qg npm package (under
// either name): the postinstall marker or the platform payload manifest. The
// manifest check mirrors DetectInstallMethod so the repository checkout (same
// name, but with a bin entry and broad platform lists) never counts.
func isNpmPackageDir(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".qg-binary-version")); err == nil {
		return true
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Name string          `json:"name"`
		OS   []string        `json:"os"`
		CPU  []string        `json:"cpu"`
		Bin  json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false
	}
	return isNpmPackageName(pkg.Name) && len(pkg.OS) == 1 && len(pkg.CPU) == 1 && len(pkg.Bin) == 0
}

// npmPrefixFromPackageRoot derives the npm --prefix from a wrapper package
// root: Unix keeps the package at <prefix>/lib/node_modules/<name> and Windows
// at <prefix>\node_modules\<name>, for either package name. A Unix path
// without the lib segment is a local install, not a global prefix, so the
// bare node_modules suffix only counts for Windows-looking paths; anything
// else reports false instead of guessing.
func npmPrefixFromPackageRoot(packageRoot string) (string, bool) {
	trimmed := strings.TrimRight(packageRoot, `/\`)
	if trimmed == "" {
		return "", false
	}
	normalized := strings.ReplaceAll(trimmed, `\`, `/`)
	for _, name := range npmPackageNames {
		scopeSuffix := "/node_modules/" + name
		libSuffix := "/lib" + scopeSuffix
		var prefixEnd int
		switch {
		case strings.HasSuffix(normalized, libSuffix):
			prefixEnd = len(normalized) - len(libSuffix)
		case strings.HasSuffix(normalized, scopeSuffix) && looksWindowsPath(trimmed):
			prefixEnd = len(normalized) - len(scopeSuffix)
		default:
			continue
		}
		if prefixEnd <= 0 {
			return "", false
		}
		// Same length as normalized: the backslash-to-slash swap is byte for byte.
		return trimmed[:prefixEnd], true
	}
	return "", false
}

// looksWindowsPath reports whether path uses Windows separators or a drive
// letter, so a bare node_modules suffix is only read as a global prefix on
// Windows-shaped paths.
func looksWindowsPath(path string) bool {
	if strings.Contains(path, `\`) {
		return true
	}
	return len(path) >= 2 && path[1] == ':' && (path[0] >= 'A' && path[0] <= 'Z' || path[0] >= 'a' && path[0] <= 'z')
}

// isNpmWrapperRoot reports whether dir is an installed wrapper package:
// .../node_modules/<name> holding the wrapper manifest of that same name, for
// either package name. The platform payload (an npm alias whose manifest
// carries the package name too) lives at .../node_modules/<name>-<os>-<arch>
// and is skipped, so the walk reaches the wrapper root and its version.
func isNpmWrapperRoot(dir string) bool {
	_, ok := npmWrapperRootName(dir)
	return ok
}

// npmWrapperRootName is the package name of the wrapper installed at dir, when
// dir is one. The path shape is checked first, so a directory that cannot be
// a wrapper root costs no read.
func npmWrapperRootName(dir string) (string, bool) {
	if filepath.Base(filepath.Dir(filepath.Dir(dir))) != "node_modules" {
		return "", false
	}
	location := filepath.Base(filepath.Dir(dir)) + "/" + filepath.Base(dir)
	if !isNpmPackageName(location) {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return "", false
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Name != location {
		return "", false
	}
	return pkg.Name, true
}

// isLegacyNpmInstall reports whether the executable belongs to an npm install
// still under npmLegacyPackageName, the install `qg upgrade` moves to
// npmPackageName.
func isLegacyNpmInstall(executablePath string) bool {
	root, ok := findNpmPackageRoot(executablePath)
	if !ok {
		return false
	}
	name, _ := npmWrapperRootName(root)
	return name == npmLegacyPackageName
}

// readNpmPackageVersion reads the version field of the wrapper package.json
// at the invoked installation root.
func readNpmPackageVersion(packageRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(packageRoot, "package.json"))
	if err != nil {
		return "", err
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", err
	}
	version := strings.TrimSpace(pkg.Version)
	if version == "" {
		return "", fmt.Errorf("package.json em %s não tem versão", packageRoot)
	}
	return version, nil
}
