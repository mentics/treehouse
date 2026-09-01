package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func FindRepoRootFrom(dir string) (string, error) {
	return runGit(dir, "rev-parse", "--show-toplevel")
}

// FindMainRepoRootFrom returns the main repository root for dir.
// For linked worktrees, it resolves the worktree root back to the owning
// repository.
func FindMainRepoRootFrom(dir string) (string, error) {
	repoRoot, err := FindRepoRootFrom(dir)
	if err != nil {
		return "", err
	}
	return mainRepoRoot(repoRoot), nil
}

func mainRepoRoot(repoRoot string) string {
	mainRoot := repoRoot
	if dir, err := runGit(repoRoot, "rev-parse", "--git-common-dir"); err == nil {
		if d, err2 := runGit(repoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir"); err2 == nil {
			dir = d
		}
		if root, ok := repoRootFromCommonGitDir(dir); ok {
			mainRoot = root
		}
	}
	return mainRoot
}

func repoRootFromCommonGitDir(dir string) (string, bool) {
	cleaned := filepath.Clean(filepath.FromSlash(dir))
	if filepath.Base(cleaned) != ".git" {
		return "", false
	}
	return filepath.Dir(cleaned), true
}

// StatusPorcelain returns non-empty porcelain lines from git status.
func StatusPorcelain(worktreePath string) ([]string, error) {
	out, err := runGit(worktreePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// PorcelainPath extracts the path from a git status porcelain line.
func PorcelainPath(line string) string {
	line = strings.TrimSuffix(line, "\r")
	if len(line) < 2 {
		return ""
	}
	var path string
	switch {
	case len(line) >= 3 && line[2] == ' ':
		path = line[3:]
	case len(line) >= 2 && line[1] == ' ':
		path = line[2:]
	default:
		return ""
	}
	path = strings.TrimSpace(path)
	if idx := strings.Index(path, " -> "); idx >= 0 {
		path = path[:idx]
	}
	return path
}

// HasTrackedChanges reports staged or unstaged changes to tracked files.
func HasTrackedChanges(worktreePath string) (bool, error) {
	out, err := runGit(worktreePath, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "??") {
			continue
		}
		return true, nil
	}
	return false, nil
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
