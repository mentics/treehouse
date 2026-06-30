package config

import (
	"testing"
)

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

func TestValidateSubmodulesMode(t *testing.T) {
	if err := ValidateSubmodulesMode("top"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSubmodulesMode("recursive"); err == nil {
		t.Fatal("expected recursive to be rejected")
	}
}
