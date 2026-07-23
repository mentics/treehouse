package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePoolDir_EmptyRoot(t *testing.T) {
	t.Setenv(EnvHome, "")
	t.Setenv(EnvWorktrees, "")

	// With empty root, pool dir should be under $HOME/.treehouse/{repoName}-{hash}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	// We need a real repo for GetRemoteURL. Use a fake approach by creating
	// a temp git repo with a remote.
	repoDir := setupGitRepo(t)

	poolDir, err := ResolvePoolDir(repoDir, "")
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	if !strings.HasPrefix(poolDir, filepath.Join(home, ".treehouse", repoName)) {
		t.Errorf("expected pool dir under %s/.treehouse/%s-*, got %s", home, repoName, poolDir)
	}
}

func TestResolvePoolDir_RelativeRoot(t *testing.T) {
	t.Setenv(EnvHome, "")
	t.Setenv(EnvWorktrees, "")
	repoDir := setupGitRepo(t)

	poolDir, err := ResolvePoolDir(repoDir, ".worktrees")
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	expected := filepath.Join(repoDir, ".worktrees", ".treehouse", repoName)
	if !strings.HasPrefix(poolDir, expected) {
		t.Errorf("expected pool dir to start with %s, got %s", expected, poolDir)
	}
}

func TestResolvePoolDir_AbsoluteRoot(t *testing.T) {
	t.Setenv(EnvHome, "")
	t.Setenv(EnvWorktrees, "")
	repoDir := setupGitRepo(t)
	absRoot := t.TempDir()

	poolDir, err := ResolvePoolDir(repoDir, absRoot)
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	expected := filepath.Join(absRoot, ".treehouse", repoName)
	if !strings.HasPrefix(poolDir, expected) {
		t.Errorf("expected pool dir to start with %s, got %s", expected, poolDir)
	}
}

func TestResolvePoolDir_DotSlashRoot(t *testing.T) {
	t.Setenv(EnvHome, "")
	t.Setenv(EnvWorktrees, "")
	repoDir := setupGitRepo(t)

	poolDir, err := ResolvePoolDir(repoDir, "./")
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	expected := filepath.Join(repoDir, ".treehouse", repoName)
	if !strings.HasPrefix(poolDir, expected) {
		t.Errorf("expected pool dir to start with %s, got %s", expected, poolDir)
	}
}

func TestResolvePoolDir_EnvVarExpansion(t *testing.T) {
	t.Setenv(EnvHome, "")
	t.Setenv(EnvWorktrees, "")
	repoDir := setupGitRepo(t)
	absRoot := t.TempDir()

	t.Setenv("TEST_TREEHOUSE_ROOT", absRoot)

	poolDir, err := ResolvePoolDir(repoDir, "$TEST_TREEHOUSE_ROOT")
	if err != nil {
		t.Fatalf("ResolvePoolDir failed: %v", err)
	}

	repoName := filepath.Base(repoDir)
	expected := filepath.Join(absRoot, ".treehouse", repoName)
	if !strings.HasPrefix(poolDir, expected) {
		t.Errorf("expected pool dir to start with %s, got %s", expected, poolDir)
	}
}

func TestResolvePoolRoot_EmptyRoot(t *testing.T) {
	t.Setenv(EnvHome, "")
	t.Setenv(EnvWorktrees, "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	poolRoot, err := ResolvePoolRoot("", "")
	if err != nil {
		t.Fatalf("ResolvePoolRoot failed: %v", err)
	}

	expected := filepath.Join(home, ".treehouse")
	if poolRoot != expected {
		t.Fatalf("expected pool root %s, got %s", expected, poolRoot)
	}
}

func TestResolvePoolRoot_RelativeRootRequiresRepo(t *testing.T) {
	t.Setenv(EnvHome, "")
	t.Setenv(EnvWorktrees, "")

	if _, err := ResolvePoolRoot("", ".worktrees"); err == nil {
		t.Fatal("expected relative root without repo to fail")
	}
}

func TestResolvePoolRoot_TreehouseHome(t *testing.T) {
	t.Setenv(EnvWorktrees, "")
	home := t.TempDir()
	t.Setenv(EnvHome, home)

	poolRoot, err := ResolvePoolRoot("", "")
	if err != nil {
		t.Fatalf("ResolvePoolRoot failed: %v", err)
	}
	if poolRoot != home {
		t.Fatalf("expected pool root %s (no .treehouse suffix), got %s", home, poolRoot)
	}
}

func TestResolvePoolRoot_TreehouseWorktrees(t *testing.T) {
	home := t.TempDir()
	worktrees := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv(EnvWorktrees, worktrees)

	poolRoot, err := ResolvePoolRoot("", "")
	if err != nil {
		t.Fatalf("ResolvePoolRoot failed: %v", err)
	}
	if poolRoot != worktrees {
		t.Fatalf("expected pool root %s, got %s", worktrees, poolRoot)
	}
}

func TestResolvePoolRoot_ConfigRootBeatsHome(t *testing.T) {
	t.Setenv(EnvWorktrees, "")
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	cfgRoot := t.TempDir()

	poolRoot, err := ResolvePoolRoot("", cfgRoot)
	if err != nil {
		t.Fatalf("ResolvePoolRoot failed: %v", err)
	}
	expected := filepath.Join(cfgRoot, ".treehouse")
	if poolRoot != expected {
		t.Fatalf("expected config root %s to win over TREEHOUSE_HOME, got %s", expected, poolRoot)
	}
}

func TestResolvePoolRoot_WorktreesBeatsConfigRoot(t *testing.T) {
	worktrees := t.TempDir()
	t.Setenv(EnvWorktrees, worktrees)
	cfgRoot := t.TempDir()

	poolRoot, err := ResolvePoolRoot("", cfgRoot)
	if err != nil {
		t.Fatalf("ResolvePoolRoot failed: %v", err)
	}
	if poolRoot != worktrees {
		t.Fatalf("expected TREEHOUSE_WORKTREES %s to win over config root, got %s", worktrees, poolRoot)
	}
}

func TestAbsoluteEnvPath_RelativeRejected(t *testing.T) {
	t.Setenv(EnvHome, "relative/path")
	if _, _, err := TreehouseHome(); err == nil {
		t.Fatal("expected relative TREEHOUSE_HOME to fail")
	}

	t.Setenv(EnvHome, "")
	t.Setenv(EnvWorktrees, "relative/worktrees")
	if _, _, err := TreehouseWorktrees(); err == nil {
		t.Fatal("expected relative TREEHOUSE_WORKTREES to fail")
	}
}

func TestUserConfigPath_DefaultAndHome(t *testing.T) {
	t.Setenv(EnvHome, "")
	userHome := t.TempDir()
	setUserHome(t, userHome)

	path, err := UserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(userHome, ".config", "treehouse", "config.toml")
	if path != expected {
		t.Fatalf("expected XDG config path %s, got %s", expected, path)
	}

	thHome := t.TempDir()
	t.Setenv(EnvHome, thHome)
	path, err = UserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	expected = filepath.Join(thHome, "config.toml")
	if path != expected {
		t.Fatalf("expected TREEHOUSE_HOME config path %s, got %s", expected, path)
	}
}

func TestDataDir_DefaultAndHome(t *testing.T) {
	t.Setenv(EnvHome, "")
	userHome := t.TempDir()
	setUserHome(t, userHome)

	dir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(userHome, ".treehouse")
	if dir != expected {
		t.Fatalf("expected data dir %s, got %s", expected, dir)
	}

	thHome := t.TempDir()
	t.Setenv(EnvHome, thHome)
	dir, err = DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if dir != thHome {
		t.Fatalf("expected data dir %s, got %s", thHome, dir)
	}
}

func TestLoad_UserConfigFromTreehouseHome(t *testing.T) {
	repoDir := t.TempDir()
	thHome := t.TempDir()
	t.Setenv(EnvHome, thHome)
	t.Setenv(EnvWorktrees, "")

	cfgTOML := `max_trees = 7
`
	if err := os.WriteFile(filepath.Join(thHome, "config.toml"), []byte(cfgTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.MaxTrees != 7 {
		t.Fatalf("MaxTrees: got %d, want 7", cfg.MaxTrees)
	}
}
