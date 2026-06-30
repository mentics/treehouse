package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupSuperprojectWithSubmodules(t *testing.T) (superDir, homeDir, subRemote, subCommit string) {
	t.Helper()

	base := t.TempDir()
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}

	homeDir = filepath.Join(base, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	subRemote = filepath.Join(base, "libfoo-remote.git")
	subDir := filepath.Join(base, "libfoo")
	superRemote := filepath.Join(base, "super-remote.git")
	superDir = filepath.Join(base, "super")

	gitCmd(t, "", "init", "--bare", "--initial-branch=main", subRemote)
	gitCmd(t, "", "clone", subRemote, subDir)
	gitCmd(t, subDir, "config", "user.email", "test@test.com")
	gitCmd(t, subDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(subDir, "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, subDir, "add", ".")
	gitCmd(t, subDir, "commit", "-m", "sub v1")
	subCommit = gitCmd(t, subDir, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(subDir, "lib.go"), []byte("package lib\n// v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, subDir, "add", ".")
	gitCmd(t, subDir, "commit", "-m", "sub v2")
	gitCmd(t, subDir, "push", "origin", "main")

	gitCmd(t, "", "init", "--bare", "--initial-branch=main", superRemote)
	gitCmd(t, "", "init", "--initial-branch=main", superDir)
	gitCmd(t, superDir, "config", "user.email", "test@test.com")
	gitCmd(t, superDir, "config", "user.name", "Test")
	gitCmd(t, superDir, "remote", "add", "origin", superRemote)
	if err := os.WriteFile(filepath.Join(superDir, "README.md"), []byte("super\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, superDir, "add", ".")
	gitCmd(t, superDir, "commit", "-m", "super initial")

	gitmodules := "[submodule \"libfoo\"]\n\tpath = vendor/libfoo\n\turl = " + subRemote + "\n"
	if err := os.WriteFile(filepath.Join(superDir, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, superDir, "update-index", "--add", "--cacheinfo", "160000,"+subCommit+",vendor/libfoo")
	gitCmd(t, superDir, "add", ".gitmodules")
	gitCmd(t, superDir, "commit", "-m", "add submodule")
	gitCmd(t, superDir, "push", "-u", "origin", "main")

	return superDir, homeDir, subRemote, subCommit
}

func writeSubmoduleUserConfig(t *testing.T, homeDir string) {
	t.Helper()
	cfgDir := filepath.Join(homeDir, ".config", "treehouse")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[submodules]\nenabled = true\nmode = \"top\"\nfetch = \"never\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGetSubmodules_CreatesChildWorktree(t *testing.T) {
	superDir, homeDir, subRemote, subCommit := setupSuperprojectWithSubmodules(t)

	stdout, stderr, code := runTreehouse(t, superDir, homeDir, nil, "get", "--lease", "--submodules")
	if code != 0 {
		t.Fatalf("get --lease --submodules failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	if wtPath == "" {
		t.Fatal("expected worktree path on stdout")
	}

	childPath := filepath.Join(wtPath, "vendor", "libfoo")
	if _, err := os.Stat(childPath); err != nil {
		t.Fatalf("expected submodule worktree at %s: %v", childPath, err)
	}

	head := gitCmd(t, childPath, "rev-parse", "HEAD")
	if head != subCommit {
		t.Fatalf("expected submodule at gitlink %s, got %s", subCommit, head)
	}

	cacheRoot := filepath.Join(homeDir, ".treehouse", "repos", "submodules")
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		t.Fatalf("expected backing repo cache: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected backing repo in cache")
	}
	_ = subRemote
}

func TestGetSubmodules_WarmReusePreservesCache(t *testing.T) {
	superDir, homeDir, _, _ := setupSuperprojectWithSubmodules(t)

	stdout, stderr, code := runTreehouse(t, superDir, homeDir, nil, "get", "--lease", "--submodules")
	if code != 0 {
		t.Fatalf("first get failed (code %d): %s", code, stderr)
	}
	firstPath := strings.TrimSpace(stdout)
	childPath := filepath.Join(firstPath, "vendor", "libfoo")
	cacheFile := filepath.Join(childPath, ".warm-cache")
	if err := os.WriteFile(cacheFile, []byte("warm\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, returnErr, code := runTreehouse(t, superDir, homeDir, nil, "return", firstPath)
	if code != 0 {
		t.Fatalf("return failed (code %d): %s", code, returnErr)
	}
	if strings.Contains(returnErr, "Aborted") {
		t.Fatalf("return aborted unexpectedly: %s", returnErr)
	}

	statusOut, statusErr, code := runTreehouse(t, superDir, homeDir, nil, "status", "--submodules")
	if code != 0 {
		t.Fatalf("status after return failed (code %d): %s", code, statusErr)
	}
	if strings.Contains(statusOut, "leased") || strings.Contains(statusOut, "in-use") {
		t.Fatalf("expected slot available after return, got:\n%s", statusOut)
	}

	stdout, stderr, code = runTreehouse(t, superDir, homeDir, nil, "get", "--lease", "--submodules")
	if code != 0 {
		t.Fatalf("second get failed (code %d): %s", code, stderr)
	}
	secondPath := strings.TrimSpace(stdout)
	if secondPath != firstPath {
		t.Fatalf("expected same slot reused, got %s then %s", firstPath, secondPath)
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("expected warm cache preserved at %s: %v", cacheFile, err)
	}
}

func TestGetSubmodules_ConfigEnabled(t *testing.T) {
	superDir, homeDir, _, subCommit := setupSuperprojectWithSubmodules(t)
	writeSubmoduleUserConfig(t, homeDir)

	stdout, stderr, code := runTreehouse(t, superDir, homeDir, nil, "get", "--lease")
	if code != 0 {
		t.Fatalf("get --lease with config submodules failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	childPath := filepath.Join(wtPath, "vendor", "libfoo")
	head := gitCmd(t, childPath, "rev-parse", "HEAD")
	if head != subCommit {
		t.Fatalf("expected gitlink checkout %s, got %s", subCommit, head)
	}
}

func TestStatusSubmodules_NestedOutput(t *testing.T) {
	superDir, homeDir, _, _ := setupSuperprojectWithSubmodules(t)

	stdout, stderr, code := runTreehouse(t, superDir, homeDir, nil, "get", "--lease", "--submodules")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)

	statusOut, statusErr, code := runTreehouse(t, superDir, homeDir, nil, "status", "--submodules")
	if code != 0 {
		t.Fatalf("status --submodules failed (code %d): %s", code, statusErr)
	}
	if !strings.Contains(statusOut, "submodule") || !strings.Contains(statusOut, "vendor/libfoo") {
		t.Fatalf("expected nested submodule status, got:\n%s", statusOut)
	}
	_ = wtPath
}

func TestPruneSubmodules_DoesNotRemoveActiveChild(t *testing.T) {
	superDir, homeDir, _, _ := setupSuperprojectWithSubmodules(t)

	stdout, stderr, code := runTreehouse(t, superDir, homeDir, nil, "get", "--lease", "--submodules")
	if code != 0 {
		t.Fatalf("get failed (code %d): %s", code, stderr)
	}
	wtPath := strings.TrimSpace(stdout)
	childPath := filepath.Join(wtPath, "vendor", "libfoo")

	_, pruneErr, code := runTreehouse(t, superDir, homeDir, nil, "prune", "--yes")
	if code != 0 {
		t.Fatalf("prune failed (code %d): %s", code, pruneErr)
	}
	if _, err := os.Stat(childPath); err != nil {
		t.Fatalf("prune removed active submodule worktree: %v", err)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("prune removed leased parent worktree: %v", err)
	}
}

func TestGetSubmodules_RecursiveRejected(t *testing.T) {
	superDir, homeDir, _, _ := setupSuperprojectWithSubmodules(t)

	_, stderr, code := runTreehouse(t, superDir, homeDir, nil, "get", "--lease", "--submodules", "--submodules-mode=recursive")
	if code == 0 {
		t.Fatal("expected recursive submodules to be rejected")
	}
	if !strings.Contains(stderr, "recursive") {
		t.Fatalf("expected recursive rejection message, got: %s", stderr)
	}
}
