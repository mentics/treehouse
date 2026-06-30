package pool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/treehouse/internal/config"
)

func TestIsRootDirtyForPoolIgnoresSubmoduleUntracked(t *testing.T) {
	base := t.TempDir()
	subRemote := filepath.Join(base, "sub-remote.git")
	subDir := filepath.Join(base, "sub")
	superDir := filepath.Join(base, "super")
	parentPath := filepath.Join(base, "wt")

	mustGitPool(t, "", "init", "--bare", "--initial-branch=main", subRemote)
	mustGitPool(t, "", "clone", subRemote, subDir)
	mustGitPool(t, subDir, "config", "user.email", "t@t.com")
	mustGitPool(t, subDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(subDir, "f.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, subDir, "add", ".")
	mustGitPool(t, subDir, "commit", "-m", "sub")
	subCommit := mustGitPool(t, subDir, "rev-parse", "HEAD")
	mustGitPool(t, subDir, "push", "origin", "main")

	mustGitPool(t, "", "init", "--initial-branch=main", superDir)
	mustGitPool(t, superDir, "config", "user.email", "t@t.com")
	mustGitPool(t, superDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(superDir, "README.md"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, superDir, "add", ".")
	mustGitPool(t, superDir, "commit", "-m", "super")
	gitmodules := "[submodule \"lib\"]\n\tpath = vendor/lib\n\turl = " + subRemote + "\n"
	if err := os.WriteFile(filepath.Join(superDir, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, superDir, "update-index", "--add", "--cacheinfo", "160000,"+subCommit+",vendor/lib")
	mustGitPool(t, superDir, "add", ".gitmodules")
	mustGitPool(t, superDir, "commit", "-m", "add sub")
	mustGitPool(t, superDir, "worktree", "add", "--detach", parentPath, "main")
	childPath := filepath.Join(parentPath, "vendor", "lib")
	mustGitPool(t, "", "clone", "--bare", subRemote, filepath.Join(base, "cache.git"))
	mustGitPool(t, filepath.Join(base, "cache.git"), "worktree", "add", "--detach", childPath, subCommit)
	if err := os.WriteFile(filepath.Join(childPath, ".warm-cache"), []byte("warm\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	state := State{Worktrees: []WorktreeEntry{
		{Name: "1", Path: parentPath, Kind: WorktreeKindRoot},
		{Name: "wt-vendor/lib", Path: childPath, Kind: WorktreeKindSubmodule, ParentPath: parentPath, SubmodulePath: "vendor/lib"},
	}}
	dirty, err := isRootDirtyForPool(parentPath, state)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Fatal("expected parent with warm submodule cache not to be dirty for pool")
	}
}

func TestReleaseClearsLeaseWithSubmoduleChildren(t *testing.T) {
	base := t.TempDir()
	subRemote := filepath.Join(base, "sub-remote.git")
	subDir := filepath.Join(base, "sub")
	superDir := filepath.Join(base, "super")
	poolDir := filepath.Join(base, "pool")

	mustGitPool(t, "", "init", "--bare", "--initial-branch=main", subRemote)
	mustGitPool(t, "", "clone", subRemote, subDir)
	mustGitPool(t, subDir, "config", "user.email", "t@t.com")
	mustGitPool(t, subDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(subDir, "f.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, subDir, "add", ".")
	mustGitPool(t, subDir, "commit", "-m", "sub")
	subCommit := mustGitPool(t, subDir, "rev-parse", "HEAD")
	mustGitPool(t, subDir, "push", "origin", "main")

	mustGitPool(t, "", "init", "--initial-branch=main", superDir)
	mustGitPool(t, superDir, "config", "user.email", "t@t.com")
	mustGitPool(t, superDir, "config", "user.name", "T")
	mustGitPool(t, superDir, "remote", "add", "origin", filepath.Join(base, "super-remote.git"))
	mustGitPool(t, "", "init", "--bare", "--initial-branch=main", filepath.Join(base, "super-remote.git"))
	if err := os.WriteFile(filepath.Join(superDir, "README.md"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, superDir, "add", ".")
	mustGitPool(t, superDir, "commit", "-m", "super")

	parentPath := filepath.Join(poolDir, "1", "super")
	if err := os.MkdirAll(filepath.Dir(parentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, superDir, "worktree", "add", "--detach", parentPath, "main")
	childPath := filepath.Join(parentPath, "vendor", "lib")
	mustGitPool(t, "", "clone", "--bare", subRemote, filepath.Join(base, "cache.git"))
	mustGitPool(t, filepath.Join(base, "cache.git"), "worktree", "add", "--detach", childPath, subCommit)

	state := State{Worktrees: []WorktreeEntry{
		{Name: "1", Path: parentPath, Kind: WorktreeKindRoot, Leased: true},
		{
			Name: "super-vendor/lib", Path: childPath, Kind: WorktreeKindSubmodule,
			ParentPath: parentPath, SubmodulePath: "vendor/lib", ExpectedCommit: subCommit,
			BackingRepoPath: filepath.Join(base, "cache.git"),
		},
	}}
	if err := WriteState(poolDir, state); err != nil {
		t.Fatal(err)
	}

	if err := Release(poolDir, parentPath, ReleaseOptions{Submodules: true}); err != nil {
		t.Fatalf("Release: %v", err)
	}

	state, err := ReadState(poolDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, wt := range state.Worktrees {
		if wt.IsRoot() && wt.Leased {
			t.Fatalf("expected lease cleared on parent, still leased")
		}
	}
}

func TestReconcileSubmodules_CreatesChildWorktree(t *testing.T) {
	base := t.TempDir()
	subRemote := filepath.Join(base, "sub-remote.git")
	subDir := filepath.Join(base, "sub")
	superDir := filepath.Join(base, "super")
	poolDir := filepath.Join(base, "pool")

	mustGitPool(t, "", "init", "--bare", "--initial-branch=main", subRemote)
	mustGitPool(t, "", "clone", subRemote, subDir)
	mustGitPool(t, subDir, "config", "user.email", "t@t.com")
	mustGitPool(t, subDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(subDir, "f.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, subDir, "add", ".")
	mustGitPool(t, subDir, "commit", "-m", "sub")
	subCommit := mustGitPool(t, subDir, "rev-parse", "HEAD")
	mustGitPool(t, subDir, "push", "origin", "main")

	mustGitPool(t, "", "init", "--initial-branch=main", superDir)
	mustGitPool(t, superDir, "config", "user.email", "t@t.com")
	mustGitPool(t, superDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(superDir, "README.md"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, superDir, "add", ".")
	mustGitPool(t, superDir, "commit", "-m", "super")
	gitmodules := "[submodule \"lib\"]\n\tpath = vendor/lib\n\turl = " + subRemote + "\n"
	if err := os.WriteFile(filepath.Join(superDir, ".gitmodules"), []byte(gitmodules), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, superDir, "update-index", "--add", "--cacheinfo", "160000,"+subCommit+",vendor/lib")
	mustGitPool(t, superDir, "add", ".gitmodules")
	mustGitPool(t, superDir, "commit", "-m", "add sub")
	mustGitPool(t, superDir, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "vendor/lib")

	parentPath := filepath.Join(poolDir, "1", "super")
	if err := os.MkdirAll(filepath.Dir(parentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGitPool(t, superDir, "worktree", "add", "--detach", parentPath, "main")

	state := State{Worktrees: []WorktreeEntry{{
		Name: "1", Path: parentPath, Kind: WorktreeKindRoot,
	}}}
	_, err := ReconcileSubmodules(SubmoduleReconcileOptions{
		SourceRepoRoot: superDir,
		ParentPath:     parentPath,
		State:          &state,
		Submodules:     config.SubmodulesConfig{Mode: "top", Fetch: "never"},
	})
	if err != nil {
		t.Fatalf("ReconcileSubmodules: %v", err)
	}

	childPath := filepath.Join(parentPath, "vendor", "lib")
	if _, err := os.Stat(childPath); err != nil {
		t.Fatalf("child worktree missing: %v", err)
	}
	if len(ChildrenOf(state, parentPath)) != 1 {
		t.Fatalf("expected 1 child entry, got %d", len(ChildrenOf(state, parentPath)))
	}
	sourceBacking := mustGitPool(t, filepath.Join(superDir, "vendor", "lib"), "rev-parse", "--git-common-dir")
	childBacking := mustGitPool(t, childPath, "rev-parse", "--git-common-dir")
	if filepath.Clean(sourceBacking) != filepath.Clean(childBacking) {
		t.Fatalf("expected shared backing repo, got %q vs %q", childBacking, sourceBacking)
	}
}

func mustGitPool(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
