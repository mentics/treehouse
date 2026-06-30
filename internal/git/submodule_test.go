package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestListSubmodulesAndGitlinkCommit(t *testing.T) {
	base := t.TempDir()
	subRemote := filepath.Join(base, "sub-remote.git")
	subDir := filepath.Join(base, "sub")
	superDir := filepath.Join(base, "super")

	mustGit(t, "", "init", "--bare", "--initial-branch=main", subRemote)
	mustGit(t, "", "clone", subRemote, subDir)
	mustGit(t, subDir, "config", "user.email", "test@test.com")
	mustGit(t, subDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(subDir, "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, subDir, "add", ".")
	mustGit(t, subDir, "commit", "-m", "sub initial")
	subCommit := strings.TrimSpace(gitOutput(t, subDir, "rev-parse", "HEAD"))
	mustGit(t, subDir, "push", "origin", "main")

	mustGit(t, "", "init", "--initial-branch=main", superDir)
	mustGit(t, superDir, "config", "user.email", "test@test.com")
	mustGit(t, superDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(superDir, "README.md"), []byte("super\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, superDir, "add", ".")
	mustGit(t, superDir, "commit", "-m", "super initial")

	gitmodules := `[submodule "libfoo"]
	path = vendor/libfoo
	url = ` + subRemote + `
`
	if err := os.WriteFile(filepath.Join(superDir, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, superDir, "update-index", "--add", "--cacheinfo", "160000,"+subCommit+",vendor/libfoo")
	mustGit(t, superDir, "add", ".gitmodules")
	mustGit(t, superDir, "commit", "-m", "add submodule")
	mustGit(t, superDir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "vendor/libfoo")

	subs, err := ListSubmodules(superDir)
	if err != nil {
		t.Fatalf("ListSubmodules: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 submodule, got %d", len(subs))
	}
	if subs[0].Path != filepath.FromSlash("vendor/libfoo") {
		t.Fatalf("unexpected path %q", subs[0].Path)
	}

	commit, err := SubmoduleGitlinkCommit(superDir, "vendor/libfoo")
	if err != nil {
		t.Fatalf("SubmoduleGitlinkCommit: %v", err)
	}
	if commit != subCommit {
		t.Fatalf("expected gitlink %s, got %s", subCommit, commit)
	}
}

func TestResolveSubmoduleRepoDir(t *testing.T) {
	base := t.TempDir()
	subRemote := filepath.Join(base, "sub-remote.git")
	subDir := filepath.Join(base, "sub")
	superDir := filepath.Join(base, "super")

	mustGit(t, "", "init", "--bare", "--initial-branch=main", subRemote)
	mustGit(t, "", "clone", subRemote, subDir)
	mustGit(t, subDir, "config", "user.email", "test@test.com")
	mustGit(t, subDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(subDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, subDir, "add", ".")
	mustGit(t, subDir, "commit", "-m", "initial")
	commit := strings.TrimSpace(gitOutput(t, subDir, "rev-parse", "HEAD"))
	mustGit(t, subDir, "push", "origin", "main")

	mustGit(t, "", "init", "--initial-branch=main", superDir)
	mustGit(t, superDir, "config", "user.email", "test@test.com")
	mustGit(t, superDir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(superDir, "README.md"), []byte("super\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, superDir, "add", ".")
	mustGit(t, superDir, "commit", "-m", "super initial")

	gitmodules := `[submodule "libfoo"]
	path = vendor/libfoo
	url = ` + subRemote + `
`
	if err := os.WriteFile(filepath.Join(superDir, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, superDir, "update-index", "--add", "--cacheinfo", "160000,"+commit+",vendor/libfoo")
	mustGit(t, superDir, "add", ".gitmodules")
	mustGit(t, superDir, "commit", "-m", "add submodule")

	_, err := ResolveSubmoduleRepoDir(superDir, "vendor/libfoo")
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("expected not initialized error, got %v", err)
	}

	mustGit(t, superDir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "vendor/libfoo")

	backing, err := ResolveSubmoduleRepoDir(superDir, "vendor/libfoo")
	if err != nil {
		t.Fatalf("ResolveSubmoduleRepoDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backing, "HEAD")); err != nil {
		t.Fatalf("backing repo missing HEAD: %v", err)
	}

	wtPath := filepath.Join(base, "slot", "vendor", "libfoo")
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AddWorktreeAtRef(backing, wtPath, commit); err != nil {
		t.Fatalf("AddWorktreeAtRef: %v", err)
	}

	head, err := HeadCommit(wtPath)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if head != commit {
		t.Fatalf("expected HEAD %s, got %s", commit, head)
	}

	cacheFile := filepath.Join(wtPath, ".warm-cache")
	if err := os.WriteFile(cacheFile, []byte("warm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ResetWorktreeToRef(wtPath, commit); err != nil {
		t.Fatalf("ResetWorktreeToRef: %v", err)
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("expected warm cache to survive reset: %v", err)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
