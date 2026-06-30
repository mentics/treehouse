package pool

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/hooks"
	"github.com/kunchenguid/treehouse/internal/process"
)

// ChildrenOf returns managed submodule entries for a parent worktree path.
func ChildrenOf(state State, parentPath string) []WorktreeEntry {
	parentPath = filepath.Clean(parentPath)
	var children []WorktreeEntry
	for _, wt := range state.Worktrees {
		if wt.IsSubmodule() && filepath.Clean(wt.ParentPath) == parentPath {
			children = append(children, wt)
		}
	}
	return children
}

func findChild(state State, parentPath, submodulePath string) *WorktreeEntry {
	parentPath = filepath.Clean(parentPath)
	submodulePath = filepath.Clean(submodulePath)
	for i := range state.Worktrees {
		wt := state.Worktrees[i]
		if wt.IsSubmodule() &&
			filepath.Clean(wt.ParentPath) == parentPath &&
			filepath.Clean(wt.SubmodulePath) == submodulePath {
			return &state.Worktrees[i]
		}
	}
	return nil
}

func removeChildEntry(state *State, childPath string) {
	childPath = filepath.Clean(childPath)
	filtered := state.Worktrees[:0]
	for _, wt := range state.Worktrees {
		if wt.IsSubmodule() && filepath.Clean(wt.Path) == childPath {
			continue
		}
		filtered = append(filtered, wt)
	}
	state.Worktrees = filtered
}

// SubmoduleReconcileOptions controls submodule preparation for a parent worktree.
type SubmoduleReconcileOptions struct {
	SourceRepoRoot string
	ParentPath     string
	State          *State
	Submodules     config.SubmodulesConfig
	PostCreate     []string
	HookStdout     io.Writer
	HookStderr     io.Writer
	OnAcquire      bool
	SetupBanner    io.Writer
}

// SubmoduleReconcileResult captures work done during reconciliation.
type SubmoduleReconcileResult struct {
	NewChildPaths  []string
	SetupPerformed bool
}

// ReconcileSubmodules ensures managed submodule worktrees exist and match gitlinks.
func ReconcileSubmodules(opts SubmoduleReconcileOptions) (SubmoduleReconcileResult, error) {
	if err := config.ValidateSubmodulesMode(opts.Submodules.Mode); err != nil {
		return SubmoduleReconcileResult{}, err
	}

	submodules, err := git.ListSubmodules(opts.ParentPath)
	if err != nil {
		return SubmoduleReconcileResult{}, err
	}

	var result SubmoduleReconcileResult
	for _, sm := range submodules {
		commit, err := git.SubmoduleGitlinkCommit(opts.ParentPath, sm.Path)
		if err != nil {
			return SubmoduleReconcileResult{}, err
		}

		childPath := filepath.Join(opts.ParentPath, sm.Path)
		existing := findChild(*opts.State, opts.ParentPath, sm.Path)

		backingPath, err := resolveBackingRepo(opts.SourceRepoRoot, sm.Path)
		if err != nil {
			return SubmoduleReconcileResult{}, fmt.Errorf("submodule %s: %w", sm.Path, err)
		}
		if err := fetchBackingRepo(backingPath, opts.Submodules.Fetch, opts.OnAcquire); err != nil {
			return SubmoduleReconcileResult{}, fmt.Errorf("submodule %s fetch: %w", sm.Path, err)
		}

		if existing == nil {
			if err := ensureChildWorktree(backingPath, childPath, commit); err != nil {
				return SubmoduleReconcileResult{}, fmt.Errorf("submodule %s: %w", sm.Path, err)
			}
			entry := WorktreeEntry{
				Name:            submoduleChildName(opts.ParentPath, sm.Path),
				Path:            childPath,
				CreatedAt:       time.Now(),
				Kind:            WorktreeKindSubmodule,
				ParentPath:      opts.ParentPath,
				SubmodulePath:   sm.Path,
				SubmoduleURL:    sm.URL,
				BackingRepoPath: backingPath,
				ExpectedCommit:  commit,
			}
			opts.State.Worktrees = append(opts.State.Worktrees, entry)
			result.NewChildPaths = append(result.NewChildPaths, childPath)
			result.SetupPerformed = true
			continue
		}

		existingIdx := indexOfWorktreePath(opts.State.Worktrees, existing.Path)
		if existingIdx < 0 {
			return SubmoduleReconcileResult{}, fmt.Errorf("submodule %s: state entry missing", sm.Path)
		}
		opts.State.Worktrees[existingIdx].ExpectedCommit = commit
		opts.State.Worktrees[existingIdx].SubmoduleURL = sm.URL
		opts.State.Worktrees[existingIdx].BackingRepoPath = backingPath

		if _, err := os.Stat(childPath); os.IsNotExist(err) {
			if err := ensureChildWorktree(backingPath, childPath, commit); err != nil {
				return SubmoduleReconcileResult{}, fmt.Errorf("submodule %s: %w", sm.Path, err)
			}
			result.SetupPerformed = true
			continue
		}
		if err := git.ResetWorktreeToRef(childPath, commit); err != nil {
			return SubmoduleReconcileResult{}, fmt.Errorf("submodule %s reset: %w", sm.Path, err)
		}
	}

	if err := cleanupRemovedSubmodules(opts); err != nil {
		return SubmoduleReconcileResult{}, err
	}

	if result.SetupPerformed && opts.SetupBanner != nil {
		fmt.Fprintln(opts.SetupBanner, "🌳 Preparing submodule worktrees for this pool slot...")
	}

	return result, nil
}

func ensureChildWorktree(backingPath, childPath, commit string) error {
	if info, err := os.Stat(childPath); err == nil {
		if info.IsDir() {
			entries, readErr := os.ReadDir(childPath)
			if readErr != nil {
				return readErr
			}
			if len(entries) == 0 {
				if err := os.Remove(childPath); err != nil {
					return err
				}
			} else if _, err := os.Stat(filepath.Join(childPath, ".git")); err == nil {
				return git.ResetWorktreeToRef(childPath, commit)
			} else {
				return fmt.Errorf("path %s exists but is not a managed submodule worktree", childPath)
			}
		} else {
			return fmt.Errorf("path %s exists but is not a managed submodule worktree", childPath)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(childPath), 0755); err != nil {
		return err
	}
	return git.AddWorktreeAtRef(backingPath, childPath, commit)
}

func indexOfWorktreePath(entries []WorktreeEntry, path string) int {
	path = filepath.Clean(path)
	for i, wt := range entries {
		if filepath.Clean(wt.Path) == path {
			return i
		}
	}
	return -1
}

func submoduleChildName(parentPath, submodulePath string) string {
	parent := filepath.Base(parentPath)
	return parent + "-" + filepath.ToSlash(submodulePath)
}

func cleanupRemovedSubmodules(opts SubmoduleReconcileOptions) error {
	current, err := git.ListSubmodules(opts.ParentPath)
	if err != nil {
		return err
	}
	currentPaths := make(map[string]struct{}, len(current))
	for _, sm := range current {
		currentPaths[filepath.Clean(sm.Path)] = struct{}{}
	}

	children := ChildrenOf(*opts.State, opts.ParentPath)
	for _, child := range children {
		if _, ok := currentPaths[filepath.Clean(child.SubmodulePath)]; ok {
			continue
		}
		if blocked, reason := childBlocksCleanup(child); blocked {
			return fmt.Errorf("removed submodule %s: %s", child.SubmodulePath, reason)
		}
		backing := child.BackingRepoPath
		if backing == "" {
			mainRoot, err := git.FindMainRepoRootFrom(child.Path)
			if err != nil {
				return err
			}
			backing = mainRoot
		}
		if err := git.RemoveCleanWorktree(backing, child.Path); err != nil {
			return fmt.Errorf("removed submodule %s: %w", child.SubmodulePath, err)
		}
		removeChildEntry(opts.State, child.Path)
	}
	return nil
}

func childBlocksCleanup(child WorktreeEntry) (bool, string) {
	if child.Leased {
		return true, "leased"
	}
	if child.OwnerPID != 0 {
		if startedAt, ok := process.StartedAt(child.OwnerPID); ok && startedAt == child.OwnerStartedAt {
			return true, "in use"
		}
	}
	procs, _ := process.FindProcessesInWorktree(child.Path)
	if len(procs) > 0 {
		return true, "running processes"
	}
	dirty, err := git.HasTrackedChanges(child.Path)
	if err != nil {
		return true, "cannot verify cleanliness"
	}
	if dirty {
		return true, "dirty"
	}
	return false, ""
}

// RunSubmodulePostCreate hooks for newly created submodule worktrees.
func RunSubmodulePostCreate(paths []string, commands []string, stdout, stderr io.Writer) {
	for _, path := range paths {
		hooks.Run(commands, path, stdout, stderr)
	}
}

// ReleaseSubmodules resets managed submodule worktrees to their expected commits.
func ReleaseSubmodules(poolDir, parentPath string) error {
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		children := ChildrenOf(state, parentPath)
		for _, child := range children {
			if child.ExpectedCommit == "" {
				continue
			}
			if _, err := os.Stat(child.Path); err != nil {
				continue
			}
			if err := git.ResetWorktreeToRef(child.Path, child.ExpectedCommit); err != nil {
				return fmt.Errorf("submodule %s: %w", child.SubmodulePath, err)
			}
		}
		for i := range state.Worktrees {
			if state.Worktrees[i].IsSubmodule() && filepath.Clean(state.Worktrees[i].ParentPath) == filepath.Clean(parentPath) {
				state.Worktrees[i].OwnerPID = 0
				state.Worktrees[i].OwnerStartedAt = 0
				state.Worktrees[i].Leased = false
				state.Worktrees[i].LeaseHolder = ""
				state.Worktrees[i].LeasedAt = time.Time{}
			}
		}
		return WriteState(poolDir, state)
	})
}

// ParentBlockedBySubmodules reports whether a parent slot cannot be reused.
func ParentBlockedBySubmodules(state State, parentPath string) (reason string, blocked bool) {
	children := ChildrenOf(state, parentPath)
	for _, child := range children {
		if child.Leased {
			return fmt.Sprintf("submodule %s is leased", child.SubmodulePath), true
		}
		if child.OwnerPID != 0 {
			if startedAt, ok := process.StartedAt(child.OwnerPID); ok && startedAt == child.OwnerStartedAt {
				return fmt.Sprintf("submodule %s is in use", child.SubmodulePath), true
			}
		}
		procs, _ := process.FindProcessesInWorktree(child.Path)
		if len(procs) > 0 {
			return fmt.Sprintf("submodule %s has running processes", child.SubmodulePath), true
		}
		if _, err := os.Stat(child.Path); err != nil {
			continue
		}
		dirty, err := git.HasTrackedChanges(child.Path)
		if err != nil {
			return fmt.Sprintf("submodule %s: cannot check status", child.SubmodulePath), true
		}
		if dirty {
			return fmt.Sprintf("submodule %s is dirty", child.SubmodulePath), true
		}
	}
	return "", false
}

// SubmoduleStatus describes one managed submodule for status output.
type SubmoduleStatus struct {
	SubmodulePath  string
	Path           string
	Status         string
	ExpectedCommit string
	HeadCommit     string
}

const (
	SubmoduleStatusWarm    = "warm"
	SubmoduleStatusClean   = "clean"
	SubmoduleStatusDirty   = "dirty"
	SubmoduleStatusInUse   = "in-use"
	SubmoduleStatusLeased  = "leased"
	SubmoduleStatusMissing = "missing"
)

// ListSubmoduleStatus returns managed submodule status for a parent worktree.
func ListSubmoduleStatus(state State, parentPath string, parentActive bool) []SubmoduleStatus {
	children := ChildrenOf(state, parentPath)
	result := make([]SubmoduleStatus, 0, len(children))
	for _, child := range children {
		cs := SubmoduleStatus{
			SubmodulePath:  child.SubmodulePath,
			Path:           child.Path,
			ExpectedCommit: shortSubmoduleSHA(child.ExpectedCommit),
		}
		if _, err := os.Stat(child.Path); os.IsNotExist(err) {
			cs.Status = SubmoduleStatusMissing
			result = append(result, cs)
			continue
		}
		head, err := git.HeadCommit(child.Path)
		if err == nil {
			cs.HeadCommit = shortSubmoduleSHA(head)
		}
		switch {
		case child.Leased:
			cs.Status = SubmoduleStatusLeased
		case child.OwnerPID != 0 && submoduleOwnerAlive(child):
			cs.Status = SubmoduleStatusInUse
		default:
			procs, _ := process.FindProcessesInWorktree(child.Path)
			if len(procs) > 0 {
				cs.Status = SubmoduleStatusInUse
			} else if dirty, _ := git.HasTrackedChanges(child.Path); dirty {
				cs.Status = SubmoduleStatusDirty
			} else if parentActive {
				cs.Status = SubmoduleStatusClean
			} else {
				cs.Status = SubmoduleStatusWarm
			}
		}
		result = append(result, cs)
	}
	return result
}

func submoduleOwnerAlive(child WorktreeEntry) bool {
	if child.OwnerPID == 0 || child.OwnerStartedAt == 0 {
		return false
	}
	startedAt, ok := process.StartedAt(child.OwnerPID)
	return ok && startedAt == child.OwnerStartedAt
}

func shortSubmoduleSHA(commit string) string {
	if len(commit) <= 7 {
		return commit
	}
	return commit[:7]
}

// DirtySubmodules returns submodule paths that are dirty under a parent.
func DirtySubmodules(state State, parentPath string) []string {
	var dirty []string
	for _, child := range ChildrenOf(state, parentPath) {
		if _, err := os.Stat(child.Path); err != nil {
			continue
		}
		isDirty, err := git.HasTrackedChanges(child.Path)
		if err == nil && isDirty {
			dirty = append(dirty, child.SubmodulePath)
		}
	}
	return dirty
}

func resolveBackingRepo(sourceRepoRoot, submodulePath string) (string, error) {
	return git.ResolveSubmoduleRepoDir(sourceRepoRoot, submodulePath)
}

func fetchBackingRepo(backingPath string, fetchPolicy string, onAcquire bool) error {
	switch fetchPolicy {
	case config.SubmoduleFetchNever:
		return nil
	case config.SubmoduleFetchAlways:
		return git.FetchRepo(backingPath)
	case config.SubmoduleFetchOnAcquire:
		if onAcquire {
			return git.FetchRepo(backingPath)
		}
		return nil
	default:
		return fmt.Errorf("unknown submodule fetch policy %q", fetchPolicy)
	}
}

func removeManagedSubmodules(state State, parentPath string, opts DestroyOptions, removed map[string]struct{}) error {
	for _, child := range ChildrenOf(state, parentPath) {
		hooks.Run(opts.PreDestroy, child.Path, os.Stdout, os.Stderr)
		backing := child.BackingRepoPath
		if backing == "" {
			resolved, err := git.FindMainRepoRootFrom(child.Path)
			if err != nil {
				return fmt.Errorf("submodule %s: %w", child.SubmodulePath, err)
			}
			backing = resolved
		}
		if err := git.RemoveWorktree(backing, child.Path); err != nil {
			return fmt.Errorf("submodule %s: %w", child.SubmodulePath, err)
		}
		removed[child.Path] = struct{}{}
	}
	return nil
}

func removePrunedSubmodules(state State, parentPath string, options PruneOptions, removed map[string]struct{}) error {
	for _, child := range ChildrenOf(state, parentPath) {
		hooks.Run(options.PreDestroy, child.Path, os.Stdout, os.Stderr)
		backing := child.BackingRepoPath
		if backing == "" {
			resolved, err := git.FindMainRepoRootFrom(child.Path)
			if err != nil {
				return fmt.Errorf("submodule %s: %w", child.SubmodulePath, err)
			}
			backing = resolved
		}
		if err := git.RemoveWorktree(backing, child.Path); err != nil {
			return fmt.Errorf("submodule %s: %w", child.SubmodulePath, err)
		}
		removed[child.Path] = struct{}{}
	}
	return nil
}
