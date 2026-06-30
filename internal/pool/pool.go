package pool

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/hooks"
	"github.com/kunchenguid/treehouse/internal/process"
)

const (
	StatusAvailable = "available"
	StatusDirty     = "dirty"
	StatusInUse     = "in-use"
	StatusLeased    = "leased"
	StatusHere      = "you're here"
)

// WorktreeStatus describes one managed worktree as reported by List.
type WorktreeStatus struct {
	Name      string
	Path      string
	Status    string
	Processes []process.ProcessInfo
	// LeaseHolder is the recorded holder for a leased worktree, if any.
	LeaseHolder string
	// Children holds nested submodule status when requested.
	Children []SubmoduleStatus
}

// acquireOptions controls how Acquire reserves the worktree it hands out.
type acquireOptions struct {
	// lease records a durable, process-independent reservation instead of the
	// default short-lived owner reservation.
	lease bool
	// leaseHolder is an optional label stored with a lease.
	leaseHolder string
	// hookStdout/hookStderr receive post-create hook output. Lease mode routes
	// hook stdout to stderr so the worktree path stays the only stdout line.
	hookStdout io.Writer
	hookStderr io.Writer
	// submodules enables managed submodule worktree pooling.
	submodules bool
	submodulesCfg config.SubmodulesConfig
}

// Acquire reserves a clean worktree from the pool with a short-lived owner
// reservation (the calling process). It is the backing call for the interactive
// `treehouse get` subshell.
func Acquire(repoRoot, poolDir string, poolSize int, postCreate []string, opts AcquireOptions) (string, error) {
	return acquire(repoRoot, poolDir, poolSize, postCreate, opts.toInternal())
}

// AcquireLease reserves a clean worktree and marks it durably LEASED so the
// reservation survives with zero processes running inside it. The lease persists
// until it is released by Release. holder is an optional label recorded with the
// lease for diagnostics. Post-create hook stdout is routed to stderr so callers
// can capture the returned path as the sole stdout line.
func AcquireLease(repoRoot, poolDir string, poolSize int, postCreate []string, holder string, opts AcquireOptions) (string, error) {
	internal := opts.toInternal()
	internal.lease = true
	internal.leaseHolder = holder
	internal.hookStdout = os.Stderr
	internal.hookStderr = os.Stderr
	return acquire(repoRoot, poolDir, poolSize, postCreate, internal)
}

// AcquireOptions configures optional acquire behavior.
type AcquireOptions struct {
	Submodules    bool
	SubmodulesCfg config.SubmodulesConfig
	HookStdout    io.Writer
	HookStderr    io.Writer
}

func (o AcquireOptions) toInternal() acquireOptions {
	stdout := o.HookStdout
	stderr := o.HookStderr
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return acquireOptions{
		hookStdout:    stdout,
		hookStderr:    stderr,
		submodules:    o.Submodules,
		submodulesCfg: o.SubmodulesCfg,
	}
}

func acquire(repoRoot, poolDir string, poolSize int, postCreate []string, opts acquireOptions) (string, error) {
	branch, err := git.GetDefaultBranch(repoRoot)
	if err != nil {
		return "", err
	}

	fmt.Fprintf(os.Stderr, "🌳 Setting up worktree...\n")
	if git.HasRemote(repoRoot, "origin") {
		if err := git.Fetch(repoRoot); err != nil {
			return "", fmt.Errorf("fetch failed: %w", err)
		}
	}

	var acquired string
	var runPostCreate bool
	var newChildPaths []string

	err = WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		state = healState(state)

		// Try to find an available worktree (clean, not in-use, not leased)
		for i, wt := range state.Worktrees {
			if !wt.IsRoot() || wt.Destroying || wt.Leased || ownerAlive(wt) {
				continue
			}
			inUse, _ := process.IsWorktreeInUse(wt.Path)
			if inUse {
				continue
			}
			dirty := false
			if opts.submodules && len(ChildrenOf(state, wt.Path)) > 0 {
				dirty, _ = isRootDirtyForPool(wt.Path, state)
			} else {
				dirty, _ = git.IsDirty(wt.Path)
			}
			if dirty {
				continue
			}
			if opts.submodules {
				if reason, blocked := ParentBlockedBySubmodules(state, wt.Path); blocked {
					_ = reason
					continue
				}
			}
			// Found an available one — reset it
			if err := git.ResetWorktree(wt.Path, branch); err != nil {
				continue
			}
			if opts.submodules {
				reconcile, err := ReconcileSubmodules(SubmoduleReconcileOptions{
					ParentPath:  wt.Path,
					State:       &state,
					Submodules:  opts.submodulesCfg,
					PostCreate:  postCreate,
					HookStdout:  opts.hookStdout,
					HookStderr:  opts.hookStderr,
					OnAcquire:   true,
					SetupBanner: os.Stderr,
				})
				if err != nil {
					continue
				}
				newChildPaths = reconcile.NewChildPaths
			}
			if err := markAcquired(&state.Worktrees[i], opts); err != nil {
				return err
			}
			acquired = wt.Path
			if err := WriteState(poolDir, state); err != nil {
				return err
			}
			runPostCreate = true
			return nil
		}

		// No available worktree — create new if pool allows
		if rootCount(state) >= poolSize {
			return fmt.Errorf("all %d worktrees are in use or dirty (max_trees = %d). Run 'treehouse status' to see details, or increase max_trees in treehouse.toml", rootCount(state), poolSize)
		}

		name := nextName(state)
		repoName := filepath.Base(repoRoot)
		wtPath := filepath.Join(poolDir, name, repoName)

		if err := os.MkdirAll(filepath.Dir(wtPath), 0755); err != nil {
			return err
		}

		if err := git.AddWorktree(repoRoot, wtPath, branch); err != nil {
			return fmt.Errorf("failed to create worktree: %w", err)
		}

		entry := WorktreeEntry{
			Name:      name,
			Path:      wtPath,
			CreatedAt: time.Now(),
			Kind:      WorktreeKindRoot,
		}
		if err := markAcquired(&entry, opts); err != nil {
			return err
		}
		state.Worktrees = append(state.Worktrees, entry)

		if opts.submodules {
			reconcile, err := ReconcileSubmodules(SubmoduleReconcileOptions{
				ParentPath:  wtPath,
				State:       &state,
				Submodules:  opts.submodulesCfg,
				PostCreate:  postCreate,
				HookStdout:  opts.hookStdout,
				HookStderr:  opts.hookStderr,
				OnAcquire:   true,
				SetupBanner: os.Stderr,
			})
			if err != nil {
				return err
			}
			newChildPaths = reconcile.NewChildPaths
		}

		acquired = wtPath
		if err := WriteState(poolDir, state); err != nil {
			return err
		}
		runPostCreate = true
		return nil
	})
	if err != nil {
		return "", err
	}
	if runPostCreate {
		hooks.Run(postCreate, acquired, opts.hookStdout, opts.hookStderr)
		RunSubmodulePostCreate(newChildPaths, postCreate, opts.hookStdout, opts.hookStderr)
	}

	return acquired, nil
}

// markAcquired stamps an acquired worktree entry: a durable lease in lease mode,
// otherwise the default short-lived owner reservation.
func markAcquired(wt *WorktreeEntry, opts acquireOptions) error {
	if opts.lease {
		wt.Leased = true
		wt.LeaseHolder = opts.leaseHolder
		wt.LeasedAt = time.Now()
		// A lease is process-independent, so it carries no owner reservation.
		wt.OwnerPID = 0
		wt.OwnerStartedAt = 0
		return nil
	}
	return reserveOwner(wt)
}

// Release resets a managed worktree, clears its short-lived owner reservation or
// durable lease, and returns it to the available pool.
func Release(poolDir, worktreePath string, opts ReleaseOptions) error {
	repoRoot, err := git.FindRepoRootFrom(worktreePath)
	if err != nil {
		return err
	}
	branch, err := git.GetDefaultBranch(repoRoot)
	if err != nil {
		return err
	}
	if err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		for _, wt := range state.Worktrees {
			if filepath.Clean(wt.Path) == filepath.Clean(worktreePath) && wt.Destroying {
				return fmt.Errorf("worktree %s is being destroyed", worktreePath)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if opts.Submodules {
		if err := ReleaseSubmodules(poolDir, worktreePath); err != nil {
			return err
		}
	}
	if err := git.ResetWorktree(worktreePath, branch); err != nil {
		return err
	}
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		found := false
		for i := range state.Worktrees {
			if filepath.Clean(state.Worktrees[i].Path) == filepath.Clean(worktreePath) {
				if state.Worktrees[i].Destroying {
					return fmt.Errorf("worktree %s is being destroyed", worktreePath)
				}
				state.Worktrees[i].OwnerPID = 0
				state.Worktrees[i].OwnerStartedAt = 0
				clearLease(&state.Worktrees[i])
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("worktree %s is not managed by treehouse", worktreePath)
		}
		return WriteState(poolDir, state)
	})
}

// ReleaseOptions configures optional release behavior.
type ReleaseOptions struct {
	Submodules bool
}

// List returns the current status of managed worktrees in poolDir.
// Leased worktrees are reported with StatusLeased and their optional holder.
func List(poolDir string, opts ListOptions) ([]WorktreeStatus, error) {
	var result []WorktreeStatus

	err := WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		state = healState(state)
		if err := WriteState(poolDir, state); err != nil {
			return err
		}

		cwd, _ := os.Getwd()

		for _, wt := range state.Worktrees {
			if !wt.IsRoot() || wt.Destroying {
				continue
			}
			ws := WorktreeStatus{
				Name:   wt.Name,
				Path:   wt.Path,
				Status: StatusAvailable,
			}

			procs, _ := process.FindProcessesInWorktree(wt.Path)
			ws.Processes = procs

			parentActive := false
			if wt.Leased {
				ws.Status = StatusLeased
				ws.LeaseHolder = wt.LeaseHolder
				parentActive = true
			} else if ownerAlive(wt) {
				ws.Status = StatusInUse
				parentActive = true
			} else if len(procs) > 0 {
				ws.Status = StatusInUse
				parentActive = true
				if cwdInWorktree(cwd, wt.Path) {
					ws.Status = StatusHere
				}
			} else {
				var dirty bool
				if len(ChildrenOf(state, wt.Path)) > 0 {
					dirty, _ = isRootDirtyForPool(wt.Path, state)
				} else {
					dirty, _ = git.IsDirty(wt.Path)
				}
				if dirty {
					ws.Status = StatusDirty
				}
			}

			if opts.IncludeSubmodules {
				ws.Children = ListSubmoduleStatus(state, wt.Path, parentActive)
				for _, child := range ws.Children {
					switch child.Status {
					case SubmoduleStatusDirty:
						ws.Status = StatusDirty
					case SubmoduleStatusInUse, SubmoduleStatusLeased:
						if ws.Status == StatusAvailable {
							ws.Status = StatusInUse
						}
					}
				}
			} else if reason, blocked := ParentBlockedBySubmodules(state, wt.Path); blocked {
				_ = reason
				if ws.Status == StatusAvailable {
					ws.Status = StatusDirty
				}
			}

			result = append(result, ws)
		}
		return nil
	})

	return result, err
}

// ListOptions configures optional list behavior.
type ListOptions struct {
	IncludeSubmodules bool
}

func FindByPath(poolDir, path string) (*WorktreeEntry, error) {
	state, err := ReadState(poolDir)
	if err != nil {
		return nil, err
	}
	for _, wt := range state.Worktrees {
		if wt.Path == path {
			return &wt, nil
		}
	}
	return nil, nil
}

func healState(state State) State {
	parentExists := map[string]bool{}
	var healed []WorktreeEntry
	for _, wt := range state.Worktrees {
		if wt.IsRoot() {
			if _, err := os.Stat(wt.Path); err == nil {
				if wt.OwnerPID != 0 && !ownerAlive(wt) {
					wt.OwnerPID = 0
					wt.OwnerStartedAt = 0
					wt.Destroying = false
				}
				healed = append(healed, wt)
				parentExists[filepath.Clean(wt.Path)] = true
			}
			continue
		}
		if wt.IsSubmodule() {
			parent := filepath.Clean(wt.ParentPath)
			if !parentExists[parent] {
				continue
			}
			if _, err := os.Stat(wt.Path); err == nil {
				if wt.OwnerPID != 0 && !ownerAlive(wt) {
					wt.OwnerPID = 0
					wt.OwnerStartedAt = 0
					wt.Destroying = false
				}
				healed = append(healed, wt)
			}
		}
	}
	state.Worktrees = healed
	return state
}

func rootCount(state State) int {
	n := 0
	for _, wt := range state.Worktrees {
		if wt.IsRoot() {
			n++
		}
	}
	return n
}

func ownerAlive(wt WorktreeEntry) bool {
	if wt.OwnerPID == 0 || wt.OwnerStartedAt == 0 {
		return false
	}
	startedAt, ok := process.StartedAt(wt.OwnerPID)
	return ok && startedAt == wt.OwnerStartedAt
}

func reserveOwner(wt *WorktreeEntry) error {
	pid := int32(os.Getpid())
	startedAt, ok := process.StartedAt(pid)
	if !ok {
		return fmt.Errorf("failed to determine owner process identity")
	}
	wt.OwnerPID = pid
	wt.OwnerStartedAt = startedAt
	return nil
}

// clearLease removes any durable lease from a worktree entry.
func clearLease(wt *WorktreeEntry) {
	wt.Leased = false
	wt.LeaseHolder = ""
	wt.LeasedAt = time.Time{}
}

func sameDestroyReservation(current, reserved WorktreeEntry) bool {
	return current.Path == reserved.Path &&
		current.Destroying &&
		current.OwnerPID == reserved.OwnerPID &&
		current.OwnerStartedAt == reserved.OwnerStartedAt
}

// RootDirtyForPool reports whether a superproject worktree should block return
// or reuse because of tracked changes. Untracked content in managed submodules
// is ignored.
func RootDirtyForPool(poolDir, parentPath string) (bool, error) {
	state, err := ReadState(poolDir)
	if err != nil {
		return false, err
	}
	if len(ChildrenOf(state, parentPath)) == 0 {
		return git.IsDirty(parentPath)
	}
	return isRootDirtyForPool(parentPath, state)
}

// isRootDirtyForPool reports whether a superproject worktree has changes that
// should block pool reuse. Managed submodule paths with only untracked content
// do not count as dirty.
func isRootDirtyForPool(parentPath string, state State) (bool, error) {
	out, err := git.StatusPorcelain(parentPath)
	if err != nil {
		return false, err
	}
	childByPath := map[string]WorktreeEntry{}
	for _, child := range ChildrenOf(state, parentPath) {
		childByPath[filepath.Clean(child.SubmodulePath)] = child
	}
	for _, line := range out {
		if line == "" {
			continue
		}
		path := git.PorcelainPath(line)
		if child, ok := childByPath[filepath.Clean(path)]; ok {
			dirty, err := git.HasTrackedChanges(child.Path)
			if err != nil {
				return true, err
			}
			if dirty {
				return true, nil
			}
			continue
		}
		return true, nil
	}
	return false, nil
}

func cwdInWorktree(cwd, worktreePath string) bool {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	absWt, err := filepath.Abs(worktreePath)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absWt, absCwd)
	if err != nil {
		return false
	}
	return rel == "." || !filepath.IsAbs(rel) && len(rel) >= 1 && rel[0] != '.'
}

func nextName(state State) string {
	max := 0
	for _, wt := range state.Worktrees {
		if !wt.IsRoot() {
			continue
		}
		if n, err := strconv.Atoi(wt.Name); err == nil && n > max {
			max = n
		}
	}
	return strconv.Itoa(max + 1)
}
