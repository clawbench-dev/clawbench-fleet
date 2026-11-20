package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestValidateUsername(t *testing.T) {
	valid := []string{"alice", "zhang_san", "li-si", "u123"}
	for _, name := range valid {
		if err := ValidateUsername(name); err != nil {
			t.Fatalf("%q should be valid: %v", name, err)
		}
	}
	invalid := []string{"", "A", "1abc", "ab", "root", "clawbench-fleet", "a b", "a/b", "a.b", strings.Repeat("a", 33)}
	for _, name := range invalid {
		if err := ValidateUsername(name); err == nil {
			t.Fatalf("%q should be invalid", name)
		}
	}
}

func TestGeneratePassword(t *testing.T) {
	p, err := GeneratePassword(12)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 12 {
		t.Fatalf("length = %d", len(p))
	}
	hasLetter, hasDigit := false, false
	for _, r := range p {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
		}
		if r >= '0' && r <= '9' {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		t.Fatalf("password %q must contain letters and digits", p)
	}
}

func TestHashPasswordMatchesClawbench(t *testing.T) {
	sum := sha256.Sum256([]byte("abc12345" + "clawbench-salt"))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got := HashPassword("abc12345"); got != want {
		t.Fatalf("hash = %s want %s", got, want)
	}
}
