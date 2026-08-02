// Command generate builds the npm packages that let tush be run with npx.
//
// npm distributes a precompiled binary as one package per platform, each
// marked with the os and cpu it belongs to, plus a wrapper package listing them
// all as optional dependencies. The package manager installs only the package
// matching the machine, so a user fetches one binary rather than all of them.
//
// The alternative — a single package whose postinstall script downloads a
// binary — is what esbuild moved away from, and for reasons that apply here:
// it breaks behind proxies and custom registries, offline, on read-only
// filesystems, and anywhere install scripts are disabled, which is increasingly
// everywhere. It also puts an unverified download outside the lockfile, on a
// package whose whole purpose is to hand somebody a shell.
//
// The platforms come from whatever is in dist/ rather than from a list kept
// here. The build matrix already exists in two places; a third copy would be
// one to forget.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// scope is the npm scope everything is published under. The wrapper is the
// scope's bare name; each platform package appends npm's own platform and
// architecture, which is what lets the shim derive the name rather than carry a
// table of them.
const scope = "@scaffoldly"

const (
	wrapperName = scope + "/tush"
	binaryName  = "tush"
	repository  = "https://github.com/scaffoldly/tush"
	license     = "MIT"
)

// npmOS and npmCPU translate Go's names for a platform into npm's. They are the
// same idea spelled differently, and getting one wrong produces a package that
// installs on nothing.
var npmOS = map[string]string{
	"darwin":  "darwin",
	"linux":   "linux",
	"windows": "win32",
}

var npmCPU = map[string]string{
	"amd64": "x64",
	"arm64": "arm64",
	"arm":   "arm",
	"386":   "ia32",
}

func main() {
	var (
		dist    = flag.String("dist", "dist", "directory holding the built binaries")
		out     = flag.String("out", filepath.Join("dist", "npm"), "where to write the packages")
		shim    = flag.String("shim", filepath.Join("npm", "shim.js"), "the wrapper's executable")
		readme  = flag.String("readme", "README.md", "what the npm page shows")
		version = flag.String("version", "", "version to publish, with or without a leading v")
	)
	flag.Parse()

	if *version == "" {
		fatal("a version is required")
	}
	semver := strings.TrimPrefix(*version, "v")

	builds, err := discover(*dist)
	if err != nil {
		fatal("%v", err)
	}
	if len(builds) == 0 {
		fatal("no binaries in %s; run `make dist` first", *dist)
	}

	if err := os.RemoveAll(*out); err != nil {
		fatal("%v", err)
	}

	var names []string
	for _, b := range builds {
		name, err := writePlatform(*out, b, semver)
		if err != nil {
			fatal("%v", err)
		}
		names = append(names, name)
		fmt.Printf("  %s (%s/%s)\n", name, b.goos, b.goarch)
	}

	if err := writeWrapper(*out, *shim, *readme, semver, names); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("  %s\n", wrapperName)
}

// build is one compiled binary and the platform it is for.
type build struct {
	path   string
	goos   string
	goarch string
}

// discover finds the binaries in dist, which are named tush_<goos>_<goarch> by
// the dist target. Anything else in there — checksums, archives, signatures —
// is not a binary and is skipped.
func discover(dist string) ([]build, error) {
	entries, err := os.ReadDir(dist)
	if err != nil {
		return nil, err
	}

	var builds []build
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		parts := strings.Split(e.Name(), "_")
		if len(parts) != 3 || parts[0] != binaryName {
			continue
		}
		goos, goarch := parts[1], parts[2]
		if _, ok := npmOS[goos]; !ok {
			continue
		}
		if _, ok := npmCPU[goarch]; !ok {
			continue
		}
		builds = append(builds, build{filepath.Join(dist, e.Name()), goos, goarch})
	}

	sort.Slice(builds, func(i, j int) bool { return builds[i].path < builds[j].path })
	return builds, nil
}

// writePlatform writes the package carrying one platform's binary, and reports
// what it is called.
func writePlatform(out string, b build, version string) (string, error) {
	goos, goarch := npmOS[b.goos], npmCPU[b.goarch]
	name := fmt.Sprintf("%s/%s-%s-%s", scope, binaryName, goos, goarch)
	dir := filepath.Join(out, fmt.Sprintf("%s-%s-%s", binaryName, goos, goarch))

	manifest := map[string]any{
		"name":        name,
		"version":     version,
		"description": fmt.Sprintf("tush binary for %s %s", goos, goarch),
		"license":     license,
		"repository":  map[string]string{"type": "git", "url": "git+" + repository + ".git"},
		"homepage":    repository,
		// What makes the package install on this platform and no other. Without
		// them every machine would fetch every binary.
		"os":  []string{goos},
		"cpu": []string{goarch},
		// Yarn's zip-backed installs cannot execute a file that is still inside
		// the archive, so ask for this one to be unpacked.
		"preferUnplugged": true,
		"files":           []string{"bin/"},
	}

	if err := writeJSON(filepath.Join(dir, "package.json"), manifest); err != nil {
		return "", err
	}

	// 0755, and deliberately so: an archive that has been through CI artifacts
	// can arrive without the executable bit, and npm preserves whatever it is
	// given.
	if err := copyFile(b.path, filepath.Join(dir, "bin", binaryName), 0o755); err != nil {
		return "", err
	}
	return name, nil
}

// writeWrapper writes the package a user actually asks for. It carries no
// binary — only the shim, and the list of packages that do.
func writeWrapper(out, shim, readme, version string, platforms []string) error {
	dir := filepath.Join(out, binaryName)

	optional := map[string]string{}
	for _, name := range platforms {
		optional[name] = version
	}

	manifest := map[string]any{
		"name":        wrapperName,
		"version":     version,
		"description": "Publish an interactive shell over a tunnel and hand back a URL",
		"license":     license,
		"repository":  map[string]string{"type": "git", "url": "git+" + repository + ".git"},
		"homepage":    repository,
		"keywords":    []string{"shell", "terminal", "tunnel", "tty", "remote", "ssh"},
		"bin":         map[string]string{binaryName: "bin/" + binaryName},
		"files":       []string{"bin/", "README.md"},
		// Exact versions: a range here would let a wrapper pair with a binary it
		// was never built alongside.
		"optionalDependencies": optional,
		"engines":              map[string]string{"node": ">=16"},
		// Deliberately no "os" field. The wrapper installs anywhere so that an
		// unsupported platform is met with an explanation from the shim rather
		// than npm's EBADPLATFORM.
	}

	if err := writeJSON(filepath.Join(dir, "package.json"), manifest); err != nil {
		return err
	}
	// The project's own README, so that the npm page says what tush is rather
	// than "This package does not have a README". It is the page most people
	// arriving through npx will see first, and npm rewrites relative links
	// against the repository field above.
	if err := copyFile(readme, filepath.Join(dir, "README.md"), 0o644); err != nil {
		return err
	}
	return copyFile(shim, filepath.Join(dir, "bin", binaryName), 0o755)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644)
}

func copyFile(from, to string, mode fs.FileMode) error {
	body, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(to, body, mode)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "npm/generate: "+format+"\n", args...)
	os.Exit(1)
}
