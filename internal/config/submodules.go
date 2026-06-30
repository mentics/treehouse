package config

import "fmt"

const (
	SubmoduleModeTop        = "top"
	SubmoduleModeRecursive  = "recursive"

	SubmoduleFetchAlways    = "always"
	SubmoduleFetchOnAcquire = "on-acquire"
	SubmoduleFetchNever     = "never"
)

type SubmodulesConfig struct {
	Enabled bool   `toml:"enabled"`
	Mode    string `toml:"mode"`
	Fetch   string `toml:"fetch"`
}

func (s SubmodulesConfig) normalized() SubmodulesConfig {
	out := s
	if out.Mode == "" {
		out.Mode = SubmoduleModeTop
	}
	if out.Fetch == "" {
		out.Fetch = SubmoduleFetchOnAcquire
	}
	return out
}

// SubmodulesActive reports whether submodule pooling should run.
func SubmodulesActive(cfg Config, cliFlag bool) bool {
	return cfg.Submodules.Enabled || cliFlag
}

// ValidateSubmodulesMode returns an error if mode is unsupported in v1.
func ValidateSubmodulesMode(mode string) error {
	if mode == "" || mode == SubmoduleModeTop {
		return nil
	}
	if mode == SubmoduleModeRecursive {
		return fmt.Errorf("recursive submodule pooling is not supported yet; use mode = \"top\"")
	}
	return fmt.Errorf("unknown submodules mode %q", mode)
}
