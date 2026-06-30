package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Submodule describes a submodule entry from .gitmodules at the current HEAD.
type Submodule struct {
	Path string
	URL  string
}

// ListSubmodules returns submodule paths and URLs from .gitmodules.
func ListSubmodules(repoRoot string) ([]Submodule, error) {
	gitmodules := filepath.Join(repoRoot, ".gitmodules")
	if _, err := os.Stat(gitmodules); os.IsNotExist(err) {
		return nil, nil
	}

	out, err := runGit(repoRoot, "config", "--file", ".gitmodules", "--get-regexp", `submodule\..*\.(path|url)`)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, nil
		}
		return nil, err
	}
	if out == "" {
		return nil, nil
	}

	byName := map[string]*Submodule{}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := fields[0]
		value := strings.Join(fields[1:], " ")
		parts := strings.Split(key, ".")
		if len(parts) < 3 || parts[0] != "submodule" {
			continue
		}
		name := parts[1]
		field := parts[len(parts)-1]
		sm, ok := byName[name]
		if !ok {
			sm = &Submodule{}
			byName[name] = sm
			order = append(order, name)
		}
		switch field {
		case "path":
			sm.Path = filepath.FromSlash(value)
		case "url":
			sm.URL = value
		}
	}

	var result []Submodule
	for _, name := range order {
		sm := byName[name]
		if sm.Path == "" || sm.URL == "" {
			continue
		}
		result = append(result, *sm)
	}
	return result, nil
}

// SubmoduleGitlinkCommit returns the commit recorded for path in HEAD.
func SubmoduleGitlinkCommit(repoRoot, submodulePath string) (string, error) {
	gitPath := filepath.ToSlash(submodulePath)
	out, err := runGit(repoRoot, "ls-tree", "HEAD", gitPath)
	if err != nil {
		return "", fmt.Errorf("submodule %s: %w", submodulePath, err)
	}
	if out == "" {
		return "", fmt.Errorf("submodule %s: not present at HEAD", submodulePath)
	}
	fields := strings.Fields(out)
	if len(fields) < 3 {
		return "", fmt.Errorf("submodule %s: unexpected ls-tree output %q", submodulePath, out)
	}
	if fields[1] != "commit" {
		return "", fmt.Errorf("submodule %s: expected gitlink, got %s", submodulePath, fields[1])
	}
	return fields[2], nil
}

// ResolveSubmoduleRepoDir returns the absolute git common dir for an initialized
// submodule checkout under sourceRepoRoot/submodulePath.
func ResolveSubmoduleRepoDir(sourceRepoRoot, submodulePath string) (string, error) {
	checkoutPath := filepath.Join(sourceRepoRoot, submodulePath)
	gitEntry := filepath.Join(checkoutPath, ".git")
	if _, err := os.Stat(gitEntry); os.IsNotExist(err) {
		return "", fmt.Errorf(
			"submodule %s is not initialized in the main repository; run git submodule update --init %s",
			submodulePath, submodulePath,
		)
	} else if err != nil {
		return "", fmt.Errorf("submodule %s: %w", submodulePath, err)
	}

	out, err := runGit(checkoutPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("submodule %s: %w", submodulePath, err)
	}
	gitDir := filepath.Clean(out)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Clean(filepath.Join(checkoutPath, gitDir))
	}
	return gitDir, nil
}

// FetchRepo fetches updates into a bare or full repository.
func FetchRepo(repoPath string) error {
	_, err := runGit(repoPath, "fetch", "--prune", "origin")
	return err
}

// AddWorktreeAtRef creates a detached worktree at ref.
func AddWorktreeAtRef(repoPath, worktreePath, ref string) error {
	_, err := runGit(repoPath, "worktree", "add", "--detach", worktreePath, ref)
	return err
}

// ResetWorktreeToRef checks out and hard-resets worktreePath to ref without cleaning untracked files.
func ResetWorktreeToRef(worktreePath, ref string) error {
	if _, err := runGit(worktreePath, "checkout", "--detach", "--force", ref); err != nil {
		return err
	}
	_, err := runGit(worktreePath, "reset", "--hard", ref)
	return err
}

// HeadCommit returns the current HEAD commit in a worktree.
func HeadCommit(worktreePath string) (string, error) {
	return runGit(worktreePath, "rev-parse", "HEAD")
}
