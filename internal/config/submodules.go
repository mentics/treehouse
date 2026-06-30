package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/treehouse/internal/git"
)

const (
	SubmoduleModeTop       = "top"
	SubmoduleModeRecursive = "recursive"

	SubmoduleFetchAlways   = "always"
	SubmoduleFetchOnAcquire = "on-acquire"
	SubmoduleFetchNever    = "never"
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

// ResolveSubmoduleCacheRoot returns the shared backing-repo cache directory.
func ResolveSubmoduleCacheRoot() (string, error) {
	poolRoot, err := ResolvePoolRoot("", "")
	if err != nil {
		return "", err
	}
	return filepath.Join(poolRoot, "repos", "submodules"), nil
}

// BackingRepoPath derives a stable cache path for a submodule URL.
func BackingRepoPath(cacheRoot, submoduleURL string) string {
	name := backingRepoName(submoduleURL)
	return filepath.Join(cacheRoot, name+".git")
}

func backingRepoName(submoduleURL string) string {
	canonical := CanonicalSubmoduleURL(submoduleURL)
	base := filepath.Base(strings.TrimSuffix(canonical, ".git"))
	if base == "" || base == "." || base == "/" {
		base = "submodule"
	}
	return base + "-" + git.ShortHash(canonical)
}

// CanonicalSubmoduleURL normalizes a submodule URL for registry keys.
func CanonicalSubmoduleURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
		return filepath.ToSlash(filepath.Clean(raw))
	}
	u, err := url.Parse(raw)
	if err != nil {
		return strings.TrimSuffix(strings.ToLower(raw), ".git")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, ".git")
	return u.String()
}
