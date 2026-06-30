package config

import (
	"path/filepath"
	"testing"
)

func TestCanonicalSubmoduleURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://github.com/org/LibFoo.git", "https://github.com/org/LibFoo"},
		{"./vendor/lib", "vendor/lib"},
	}
	for _, tc := range tests {
		got := CanonicalSubmoduleURL(tc.in)
		if got != tc.want {
			t.Errorf("CanonicalSubmoduleURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSubmodulesActive(t *testing.T) {
	cfg := Config{Submodules: SubmodulesConfig{Enabled: true}}
	if !SubmodulesActive(cfg, false) {
		t.Fatal("expected config enabled")
	}
	if !SubmodulesActive(Config{}, true) {
		t.Fatal("expected cli flag")
	}
	if SubmodulesActive(Config{}, false) {
		t.Fatal("expected inactive")
	}
}

func TestBackingRepoPathStable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	p1 := BackingRepoPath(root, "https://example.com/foo.git")
	p2 := BackingRepoPath(root, "https://example.com/foo")
	if p1 != p2 {
		t.Fatalf("expected stable path, got %q and %q", p1, p2)
	}
}

func TestValidateSubmodulesMode(t *testing.T) {
	if err := ValidateSubmodulesMode("top"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSubmodulesMode("recursive"); err == nil {
		t.Fatal("expected recursive to be rejected")
	}
}
