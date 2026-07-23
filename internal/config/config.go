package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/kunchenguid/treehouse/internal/git"
)

const (
	EnvHome      = "TREEHOUSE_HOME"
	EnvWorktrees = "TREEHOUSE_WORKTREES"
)

type Config struct {
	MaxTrees   int              `toml:"max_trees"`
	Root       string           `toml:"root"`
	Hooks      Hooks            `toml:"hooks,omitempty"`
	Submodules SubmodulesConfig `toml:"submodules,omitempty"`
}

type Hooks struct {
	PostCreate []string `toml:"post_create,omitempty"`
	PreDestroy []string `toml:"pre_destroy,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		MaxTrees: 16,
	}
}

func Load(repoRoot string) (Config, error) {
	cfg := DefaultConfig()

	repoPath := filepath.Join(repoRoot, "treehouse.toml")
	hasRepoConfig := false
	if _, err := os.Stat(repoPath); err == nil {
		hasRepoConfig = true
		if _, err := toml.DecodeFile(repoPath, &cfg); err != nil {
			return cfg, err
		}
		cfg.Hooks = Hooks{}
	}

	userCfg, hasUserConfig, err := loadUser()
	if err != nil {
		return cfg, err
	}
	if hasUserConfig {
		if !hasRepoConfig {
			cfg = userCfg
		} else {
			cfg.Hooks = userCfg.Hooks
		}
	}

	cfg.Submodules = cfg.Submodules.normalized()
	return cfg, nil
}

// LoadGlobal returns the default configuration merged with user-level config.
// It intentionally ignores repo-level config because callers may run without a
// repository context.
func LoadGlobal() (Config, error) {
	cfg := DefaultConfig()
	userCfg, hasUserConfig, err := loadUser()
	if err != nil {
		return cfg, err
	}
	if hasUserConfig {
		cfg = userCfg
	}
	cfg.Submodules = cfg.Submodules.normalized()
	return cfg, nil
}

func loadUser() (Config, bool, error) {
	cfg := DefaultConfig()
	userPath, err := UserConfigPath()
	if err != nil {
		return cfg, false, err
	}
	if _, err := os.Stat(userPath); err == nil {
		if _, err := toml.DecodeFile(userPath, &cfg); err != nil {
			return cfg, false, err
		}
		return cfg, true, nil
	}
	return cfg, false, nil
}

// absoluteEnvPath reads name from the environment, expands embedded variables,
// and requires the result to be absolute. An unset or empty value returns ok=false.
func absoluteEnvPath(name string) (path string, ok bool, err error) {
	raw := os.Getenv(name)
	if raw == "" {
		return "", false, nil
	}
	expanded := os.ExpandEnv(raw)
	if !filepath.IsAbs(expanded) {
		return "", true, fmt.Errorf("%s must be an absolute path, got %q", name, expanded)
	}
	return filepath.Clean(expanded), true, nil
}

// TreehouseHome returns the absolute TREEHOUSE_HOME value when set.
func TreehouseHome() (path string, ok bool, err error) {
	return absoluteEnvPath(EnvHome)
}

// TreehouseWorktrees returns the absolute TREEHOUSE_WORKTREES value when set.
func TreehouseWorktrees() (path string, ok bool, err error) {
	return absoluteEnvPath(EnvWorktrees)
}

// UserConfigPath returns the user-level config.toml path.
// When TREEHOUSE_HOME is set this is $TREEHOUSE_HOME/config.toml; otherwise
// ~/.config/treehouse/config.toml.
func UserConfigPath() (string, error) {
	home, set, err := TreehouseHome()
	if err != nil {
		return "", err
	}
	if set {
		return filepath.Join(home, "config.toml"), nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".config", "treehouse", "config.toml"), nil
}

// DataDir returns the durable Treehouse data directory: TREEHOUSE_HOME when set,
// otherwise $HOME/.treehouse. Used for update-check cache and similar state.
func DataDir() (string, error) {
	home, set, err := TreehouseHome()
	if err != nil {
		return "", err
	}
	if set {
		return home, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".treehouse"), nil
}

func ResolvePoolDir(repoRoot string, root string) (string, error) {
	// Use remote URL for the hash when available; fall back to the
	// absolute repo path for purely-local repositories.
	hashInput, err := git.GetRemoteURL(repoRoot)
	if err != nil {
		hashInput = repoRoot
	}

	repoName := filepath.Base(repoRoot)
	shortHash := git.ShortHash(hashInput)
	poolName := repoName + "-" + shortHash

	poolRoot, err := ResolvePoolRoot(repoRoot, root)
	if err != nil {
		return "", err
	}
	return filepath.Join(poolRoot, poolName), nil
}

// ResolvePoolRoot resolves the directory that contains per-repository pools.
//
// Precedence: TREEHOUSE_WORKTREES > config root > TREEHOUSE_HOME > $HOME/.treehouse.
// Env-based paths are used directly (no ".treehouse" suffix). Config root and the
// legacy default still append ".treehouse". Relative config roots require repoRoot.
func ResolvePoolRoot(repoRoot string, root string) (string, error) {
	if worktrees, set, err := TreehouseWorktrees(); err != nil {
		return "", err
	} else if set {
		return worktrees, nil
	}

	if root != "" {
		expanded := os.ExpandEnv(root)
		if !filepath.IsAbs(expanded) {
			if repoRoot == "" {
				return "", fmt.Errorf("relative treehouse root %q requires a repository", root)
			}
			expanded = filepath.Join(repoRoot, expanded)
		}
		return filepath.Join(expanded, ".treehouse"), nil
	}

	if home, set, err := TreehouseHome(); err != nil {
		return "", err
	} else if set {
		return home, nil
	}

	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".treehouse"), nil
}
