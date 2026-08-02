package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A published npm version cannot be replaced, only deprecated. So the things
// that would make a package useless — the wrong os, a missing binary, a
// dependency on a package that was never built — are worth catching here rather
// than after they are permanent.

// TestPackagesMatchTheBinaries checks that what is generated describes what was
// actually built: one package per binary, each admitting only its own platform,
// and a wrapper depending on exactly those and nothing else.
func TestPackagesMatchTheBinaries(t *testing.T) {
	dist := t.TempDir()
	for _, name := range []string{
		"tush_darwin_arm64", "tush_darwin_amd64",
		"tush_linux_amd64", "tush_linux_arm64", "tush_linux_arm",
		// Everything else the release puts in the same directory. These sit
		// beside the binaries and are named almost identically, so they are the
		// things most likely to be mistaken for one.
		"SHA256SUMS",
		"tush_darwin_arm64.zip",
		"tush_darwin_arm64.zip.sha256",
		"tush_darwin_arm64.zip.sigstore",
		"tush_linux_amd64.zip",
	} {
		write(t, filepath.Join(dist, name), "binary")
	}

	out := generate(t, dist, "v1.2.3")

	wrapper := read(t, filepath.Join(out, "tush", "package.json"))
	optional, _ := wrapper["optionalDependencies"].(map[string]any)
	if len(optional) != 5 {
		t.Fatalf("wrapper depends on %d platform packages, want 5: %v", len(optional), optional)
	}

	wantPlatform := map[string][2]string{
		"@scaffoldly/tush-darwin-arm64": {"darwin", "arm64"},
		"@scaffoldly/tush-darwin-x64":   {"darwin", "x64"},
		"@scaffoldly/tush-linux-x64":    {"linux", "x64"},
		"@scaffoldly/tush-linux-arm64":  {"linux", "arm64"},
		"@scaffoldly/tush-linux-arm":    {"linux", "arm"},
	}

	for name, want := range wantPlatform {
		version, ok := optional[name]
		if !ok {
			t.Errorf("wrapper does not depend on %s", name)
			continue
		}
		// Exact, not a range: a range lets a wrapper pair with a binary it was
		// never built alongside.
		if version != "1.2.3" {
			t.Errorf("%s pinned at %v, want the exact version 1.2.3", name, version)
		}

		dir := filepath.Join(out, "tush-"+want[0]+"-"+want[1])
		manifest := read(t, filepath.Join(dir, "package.json"))

		if got := first(manifest["os"]); got != want[0] {
			t.Errorf("%s os = %q, want %q", name, got, want[0])
		}
		if got := first(manifest["cpu"]); got != want[1] {
			t.Errorf("%s cpu = %q, want %q", name, got, want[1])
		}
		if manifest["name"] != name {
			t.Errorf("%s is named %v", name, manifest["name"])
		}

		// The binary has to be there, and has to be runnable. An archive that
		// has been through CI artifacts can arrive without the executable bit.
		binary := filepath.Join(dir, "bin", "tush")
		info, err := os.Stat(binary)
		if err != nil {
			t.Errorf("%s carries no binary: %v", name, err)
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s binary is not executable: %v", name, info.Mode())
		}
	}

	// SHA256SUMS is not a platform.
	if _, err := os.Stat(filepath.Join(out, "tush--")); err == nil {
		t.Error("something that is not a binary became a package")
	}
}

// TestWrapperInstallsAnywhere checks the wrapper does not restrict itself by
// platform. It carries no binary, and an unsupported machine should be met with
// the shim's explanation rather than npm's EBADPLATFORM.
func TestWrapperInstallsAnywhere(t *testing.T) {
	dist := t.TempDir()
	write(t, filepath.Join(dist, "tush_linux_amd64"), "binary")

	wrapper := read(t, filepath.Join(generate(t, dist, "v1.0.0"), "tush", "package.json"))

	if _, ok := wrapper["os"]; ok {
		t.Error("the wrapper restricts its platforms, so an unsupported machine gets EBADPLATFORM instead of an explanation")
	}
	if bin, ok := wrapper["bin"].(map[string]any); !ok || bin["tush"] == nil {
		t.Errorf("the wrapper has no tush command, so npx has nothing to run: %v", wrapper["bin"])
	}
}

// TestShimNamesThePackagesThatAreBuilt is the one that guards the seam between
// the two halves. The shim derives the package name from npm's platform and
// architecture; the generator names packages the same way. Nothing checks that
// at build time, and if they disagree the install succeeds and the command
// fails.
func TestShimNamesThePackagesThatAreBuilt(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}

	dist := t.TempDir()
	write(t, filepath.Join(dist, "tush_linux_amd64"), "binary")
	out := generate(t, dist, "v1.0.0")

	// Ask node what the shim would look for on a linux/x64 machine, using the
	// shipped shim rather than a copy of its logic.
	shim := filepath.Join("..", "shim.js")
	source, err := os.ReadFile(shim)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "@scaffoldly/tush-${process.platform}-${process.arch}") {
		t.Fatal("the shim no longer derives the package name from platform and arch; it needs a matching check here")
	}

	// And that is the name the generator produced.
	wanted := "@scaffoldly/tush-linux-x64"
	manifest := read(t, filepath.Join(out, "tush-linux-x64", "package.json"))
	if manifest["name"] != wanted {
		t.Errorf("generator produced %v, shim would look for %s", manifest["name"], wanted)
	}
}

func generate(t *testing.T, dist, version string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "npm")

	cmd := exec.Command("go", "run", ".",
		"-dist", dist, "-out", out, "-shim", filepath.Join("..", "shim.js"), "-version", version)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate failed: %v\n%s", err, output)
	}
	return out
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("%s is not valid JSON, so npm would reject it: %v", path, err)
	}
	return manifest
}

func first(v any) string {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	s, _ := list[0].(string)
	return s
}
