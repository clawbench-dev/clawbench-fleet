package sysops

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseNpmVersions(t *testing.T) {
	list, err := parseNpmVersions(`["0.141.0","0.142.0","0.143.0"]`)
	if err != nil || len(list) != 3 || list[2] != "0.143.0" {
		t.Fatalf("array: %v %v", list, err)
	}
	single, err := parseNpmVersions(`"0.143.0"`)
	if err != nil || len(single) != 1 || single[0] != "0.143.0" {
		t.Fatalf("single: %v %v", single, err)
	}
	if _, err := parseNpmVersions(""); err == nil {
		t.Fatal("empty output should be an error")
	}
	if _, err := parseNpmVersions("npm error code E404"); err == nil {
		t.Fatal("non-JSON output should be an error")
	}
}

func TestNpmPlatformPackage(t *testing.T) {
	pkg, err := NpmPlatformPackage("@xulongzhe/clawbench")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"linux/amd64":   "@xulongzhe/clawbench-linux-x64",
		"linux/arm64":   "@xulongzhe/clawbench-linux-arm64",
		"darwin/amd64":  "@xulongzhe/clawbench-darwin-x64",
		"darwin/arm64":  "@xulongzhe/clawbench-darwin-arm64",
		"windows/amd64": "@xulongzhe/clawbench-win32-x64",
	}[runtime.GOOS+"/"+runtime.GOARCH]
	if want == "" {
		t.Skipf("unsupported platform %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if pkg != want {
		t.Fatalf("got %s want %s", pkg, want)
	}

	bin, err := NpmBinaryPath("/tmp/prefix", "@xulongzhe/clawbench")
	if err != nil {
		t.Fatal(err)
	}
	scope, name, _ := strings.Cut(pkg, "/")
	wantBin := filepath.Join("/tmp/prefix", "node_modules", scope, name, "bin", "clawbench")
	if bin != wantBin {
		t.Fatalf("got %s want %s", bin, wantBin)
	}

	if _, err := NpmPlatformPackage("clawbench"); err == nil {
		t.Fatal("unscoped package should be rejected")
	}
}

// TestNpmPlatformPackageRejectsEmptyName covers the "@scope/" form, which is a
// typo an operator can easily make in config.yaml. It has to be reported
// instead of producing a package like "@scope/-linux-x64".
func TestNpmPlatformPackageRejectsEmptyName(t *testing.T) {
	for _, pkg := range []string{"@scope/", "@scope/ ", "  "} {
		if _, err := NpmPlatformPackage(pkg); err == nil {
			t.Fatalf("%q should be rejected", pkg)
		}
	}
}
