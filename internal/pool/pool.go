package pool

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/hooks"
	"github.com/kunchenguid/treehouse/internal/process"
	"github.com/kunchenguid/treehouse/internal/vcs"
)

const (
	StatusAvailable = "available"
	StatusDirty     = "dirty"
	StatusInUse     = "in-use"
	StatusLeased    = "leased"
	StatusHere      = "you're here"
	StatusDamaged   = "damaged"
)

// WorktreeStatus describes one managed worktree as reported by List.
type WorktreeStatus struct {
	Name   string
	Path   string
	Status string
	// Flavor is the backend the worktree's own marker identifies ("git" or
	// "jj"), independent of what the repository currently selects.
	Flavor    string
	Processes []process.ProcessInfo
	// LeaseID identifies the current acquisition of a leased worktree.
	LeaseID string
	// LeaseHolder is the recorded holder for a leased worktree, if any.
	LeaseHolder string
	// LeasedAt records when the current lease was acquired.
	LeasedAt time.Time
	// Children holds nested submodule status when requested.
	Children []SubmoduleStatus
}

// LeaseInfo is the stable machine-readable identity of one lease acquisition.
type LeaseInfo struct {
	Path        string    `json:"path"`
	LeaseID     string    `json:"lease_id"`
	LeaseHolder string    `json:"lease_holder"`
	LeasedAt    time.Time `json:"leased_at"`
	// BaseBranch is the branch this acquisition was cut from, explicit or
	// inferred. Always populated, never persisted: it describes one
	// acquisition, and the next reset resolves the branch again.
	BaseBranch string `json:"base_branch"`
}

// AcquireOptions controls optional acquisition behavior.
type AcquireOptions struct {
	// SkipFetch uses the repository's existing local refs instead of fetching
	// origin before acquiring a worktree.
	SkipFetch bool
	// BaseBranch overrides the branch worktrees are cut from. Empty keeps the
	// branch inferred from the repository. A non-empty value that cannot be
	// resolved fails the acquisition rather than falling back.
	BaseBranch string
	// Submodules enables managed submodule worktree pooling.
	Submodules bool
	SubmodulesCfg config.SubmodulesConfig
	// HookStdout/HookStderr receive post-create hook output when set.
	HookStdout io.Writer
	HookStderr io.Writer
}

// acquireOptions controls how Acquire reserves the worktree it hands out.
type acquireOptions struct {
	// skipFetch uses existing local refs without contacting origin.
	skipFetch bool
	// baseBranch is the explicitly requested base branch, or empty to infer it.
	baseBranch string
	// lease records a durable, process-independent reservation instead of the
	// default short-lived owner reservation.
	lease bool
	// leaseHolder is an optional label stored with a lease.
	leaseHolder string
	// hookStdout/hookStderr receive post-create hook output. Lease mode routes
	// hook stdout to stderr so it cannot contaminate machine-readable CLI output.
	hookStdout io.Writer
	hookStderr io.Writer
	// submodules enables managed submodule worktree pooling.
	submodules    bool
	submodulesCfg config.SubmodulesConfig
}

func (o AcquireOptions) toInternal(lease bool, holder string, stdout, stderr io.Writer) acquireOptions {
	if stdout == nil {
		stdout = o.HookStdout
	}
	if stderr == nil {
		stderr = o.HookStderr
	}
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	return acquireOptions{
		skipFetch:     o.SkipFetch,
		baseBranch:    o.BaseBranch,
		lease:         lease,
		leaseHolder:   holder,
		hookStdout:    stdout,
		hookStderr:    stderr,
		submodules:    o.Submodules,
		submodulesCfg: o.SubmodulesCfg,
	}
}

// Acquire reserves a clean worktree from the pool with a short-lived owner
// reservation (the calling process). It is the backing call for the interactive
// `treehouse get` subshell.
func Acquire(repoRoot, poolDir string, poolSize int, postCreate []string) (string, error) {
	return AcquireWithOptions(repoRoot, poolDir, poolSize, postCreate, AcquireOptions{})
}

// AcquireWithOptions reserves a clean worktree with optional acquisition behavior.
func AcquireWithOptions(repoRoot, poolDir string, poolSize int, postCreate []string, options AcquireOptions) (string, error) {
	acquired, err := acquire(repoRoot, poolDir, poolSize, postCreate, options.toInternal(false, "", os.Stdout, os.Stderr))
	return acquired.Path, err
}

// AcquireLease reserves a clean worktree and marks it durably LEASED so the
// reservation survives with zero processes running inside it. The lease persists
// until it is released by Release. holder is an optional label recorded with the
// lease for diagnostics. Post-create hook stdout is routed to stderr so callers
// can emit machine-readable allocation output without hook output on stdout.
func AcquireLease(repoRoot, poolDir string, poolSize int, postCreate []string, holder string) (string, error) {
	lease, err := AcquireLeaseInfo(repoRoot, poolDir, poolSize, postCreate, holder)
	return lease.Path, err
}

// AcquireLeaseInfo reserves a worktree exactly like AcquireLease and returns
// the immutable identity and metadata for that acquisition.
func AcquireLeaseInfo(repoRoot, poolDir string, poolSize int, postCreate []string, holder string) (LeaseInfo, error) {
	return AcquireLeaseInfoWithOptions(repoRoot, poolDir, poolSize, postCreate, holder, AcquireOptions{})
}

// AcquireLeaseInfoWithOptions reserves a durable lease with optional acquisition behavior.
func AcquireLeaseInfoWithOptions(repoRoot, poolDir string, poolSize int, postCreate []string, holder string, options AcquireOptions) (LeaseInfo, error) {
	return acquire(repoRoot, poolDir, poolSize, postCreate, options.toInternal(true, holder, os.Stderr, os.Stderr))
}

func acquire(repoRoot, poolDir string, poolSize int, postCreate []string, opts acquireOptions) (LeaseInfo, error) {
	fmt.Fprintf(os.Stderr, "🌳 Setting up worktree...\n")
	if !opts.skipFetch && vcs.HasRemote(repoRoot, "origin") {
		if err := vcs.Fetch(repoRoot); err != nil {
			return LeaseInfo{}, fmt.Errorf("fetch failed: %w", err)
		}
	}

	// After the fetch, not before: a base that exists only on origin would be
	// rejected against pre-fetch refs.
	branch, err := resolveBaseBranch(repoRoot, opts.baseBranch)
	if err != nil {
		return LeaseInfo{}, err
	}

	var acquired LeaseInfo
	var runPostCreate bool
	var newChildPaths []string

	err = WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		state = healState(state)

		// Try to find an available worktree (clean, not in-use, not leased,
		// and of the flavor the repository currently selects: a caller who
		// opted in to jj must not be handed a git worktree where jj commands
		// do not work, and vice versa; other-flavor slots are left intact
		// and leave the pool via the documented migration, destroy then
		// re-acquire).
		wantFlavor := vcs.BackendNameFor(repoRoot)
		otherFlavor := 0
		for i, wt := range state.Worktrees {
			if !wt.IsRoot() || wt.Destroying || wt.Leased || ownerAlive(wt) {
				continue
			}
			flavor := vcs.WorktreeBackendName(wt.Path)
			if flavor == "" {
				// No .git or .jj marker: the slot is damaged or missing.
				// Every dispatch on such a path falls back to the
				// configured backend, which in an in-project pool resolves
				// the repository ENCLOSING the pool - the safety checks
				// would vouch for that repository and the reset would
				// rewrite it. Fail closed and leave the slot for destroy,
				// which classifies it unverified and removes it only with
				// --include-unlanded; prune skips it as unverifiable and
				// neither path ever resets it.
				continue
			}
			if flavor != wantFlavor {
				otherFlavor++
				continue
			}
			inUse, _ := process.IsWorktreeInUse(wt.Path)
			if inUse {
				continue
			}
			// Skip a slot that carries unlanded work. A crashed or rebooted owner
			// leaves the reservation empty while its worktree still holds committed
			// commits (a clean tree passes IsDirty), so availability alone must not
			// authorize a reset. Fail closed: if either the working tree or the
			// merge state cannot be proven safe, leave the slot untouched rather
			// than let ResetWorktree discard the work.
			dirty, err := rootWorktreeDirty(wt, state)
			if err != nil || dirty {
				continue
			}
			if opts.submodules {
				if reason, blocked := ParentBlockedBySubmodules(state, wt.Path); blocked {
					_ = reason
					continue
				}
			}
			safe, resetRef, head, err := vcs.IsWorktreeSafeToReset(wt.Path, branch)
			if err != nil {
				continue
			}
			if !safe && !headMergedIntoRecordedBase(wt, branch, head) {
				continue
			}
			// Found an available one. Reset it to the verified commit only if
			// HEAD is still the one whose ancestry was checked and the tree is
			// still clean under the exclusive lock.
			requireClean := len(ChildrenOf(state, wt.Path)) == 0
			if err := vcs.ResetWorktreeToRef(wt.Path, resetRef, head, requireClean); err != nil {
				continue
			}
			state.Worktrees[i].BaseBranch = opts.baseBranch
			if opts.submodules {
				reconcile, err := ReconcileSubmodules(SubmoduleReconcileOptions{
					SourceRepoRoot: repoRoot,
					ParentPath:     wt.Path,
					State:          &state,
					Submodules:     opts.submodulesCfg,
					PostCreate:     postCreate,
					HookStdout:     opts.hookStdout,
					HookStderr:     opts.hookStderr,
					OnAcquire:      true,
					SetupBanner:    os.Stderr,
				})
				if err != nil {
					continue
				}
				newChildPaths = reconcile.NewChildPaths
			}
			if err := markAcquired(&state.Worktrees[i], opts); err != nil {
				return err
			}
			acquired = leaseInfoFromEntry(state.Worktrees[i], branch)
			if err := WriteState(poolDir, state); err != nil {
				return err
			}
			runPostCreate = true
			return nil
		}

		// No available worktree — create new if pool allows
		if rootCount(state) >= poolSize {
			if otherFlavor > 0 {
				return fmt.Errorf("all %d worktrees are in use, dirty, or hold the other backend's worktrees (%d %s-flavored; the repository selects %s). Run 'treehouse status' to see details, destroy old-flavor worktrees to migrate the pool, or increase max_trees in treehouse.toml", rootCount(state), otherFlavor, map[string]string{"git": "jj", "jj": "git"}[wantFlavor], wantFlavor)
			}
			return fmt.Errorf("all %d worktrees are in use or dirty (max_trees = %d). Run 'treehouse status' to see details, or increase max_trees in treehouse.toml", rootCount(state), poolSize)
		}

		name := nextName(state)
		repoName := filepath.Base(repoRoot)
		wtPath := filepath.Join(poolDir, name, repoName)

		if err := os.MkdirAll(filepath.Dir(wtPath), 0755); err != nil {
			return err
		}

		// Clear any stale worktree bookkeeping left behind by a crashed or
		// forcibly removed worktree. Without this, git rejects the add with
		// "missing but already registered worktree". Prune is safe: it only
		// removes registrations whose target directories are already gone.
		//
		// Best-effort: prune is a self-healing optimization, not a precondition
		// for AddWorktree in the common (non-stale) case. A transient failure
		// (e.g. a temporary .git/worktrees lock or permission issue) must not
		// wedge a get that would otherwise succeed; let AddWorktree surface the
		// real error if one exists.
		if err := vcs.PruneWorktrees(repoRoot); err != nil {
			fmt.Fprintf(os.Stderr, "🌳 Warning: failed to prune stale worktrees: %v\n", err)
		}

		if err := vcs.AddWorktree(repoRoot, wtPath, branch); err != nil {
			return fmt.Errorf("failed to create worktree: %w", err)
		}

		entry := WorktreeEntry{
			Name:       name,
			Path:       wtPath,
			CreatedAt:  time.Now(),
			Kind:       WorktreeKindRoot,
			BaseBranch: opts.baseBranch,
		}
		if err := markAcquired(&entry, opts); err != nil {
			return err
		}
		state.Worktrees = append(state.Worktrees, entry)

		if opts.submodules {
			reconcile, err := ReconcileSubmodules(SubmoduleReconcileOptions{
				SourceRepoRoot: repoRoot,
				ParentPath:     wtPath,
				State:          &state,
				Submodules:     opts.submodulesCfg,
				PostCreate:     postCreate,
				HookStdout:     opts.hookStdout,
				HookStderr:     opts.hookStderr,
				OnAcquire:      true,
				SetupBanner:    os.Stderr,
			})
			if err != nil {
				return err
			}
			newChildPaths = reconcile.NewChildPaths
		}

		acquired = leaseInfoFromEntry(entry, branch)
		if err := WriteState(poolDir, state); err != nil {
			return err
		}
		runPostCreate = true
		return nil
	})
	if err != nil {
		return LeaseInfo{}, err
	}
	if runPostCreate {
		hooks.Run(postCreate, acquired.Path, opts.hookStdout, opts.hookStderr)
		RunSubmodulePostCreate(newChildPaths, postCreate, opts.hookStdout, opts.hookStderr)
	}

	return acquired, nil
}

func leaseInfoFromEntry(wt WorktreeEntry, baseBranch string) LeaseInfo {
	return LeaseInfo{
		Path:        wt.Path,
		LeaseID:     wt.LeaseID,
		LeaseHolder: wt.LeaseHolder,
		LeasedAt:    wt.LeasedAt,
		BaseBranch:  baseBranch,
	}
}

// headMergedIntoRecordedBase reports whether a slot carries nothing beyond the
// base it was parked on. Acquisitions that mix bases would otherwise wedge the
// pool: a slot returned to develop is not merged into main, so a later plain
// get skips it and builds a new slot until max_trees, with nothing able to
// reclaim it. Work that only exists in the slot's own base is as disposable as
// work in the requested one; a slot holding commits beyond it is not, and is
// still skipped.
//
// An entry written before base_branch existed, and any inferred acquisition,
// records no base, so the repository default stands in as its implicit base
// HERE ONLY: prune and destroy deliberately give such a slot no second
// reading and stay on the origin-validated default ref. The asymmetry is
// safe because acquire only RESETS a slot whose HEAD stays reachable from a
// local branch, while prune and destroy DELETE and so must stay conservative
// - which is exactly what keeps non-opt-in pools on the pre-feature deletion
// semantics.
//
// Fails closed on an unresolvable base, an errored check, and a HEAD that moved
// between the two readings. A base equal to the requested branch answers false
// without asking git again: the caller reaches this only after that same query
// returned unsafe.
func headMergedIntoRecordedBase(wt WorktreeEntry, requested, head string) bool {
	base := wt.BaseBranch
	if base == "" {
		resolved, err := vcs.DefaultBranchForWorktree(wt.Path)
		if err != nil {
			return false
		}
		base = resolved
	}
	if base == requested {
		return false
	}
	safe, _, recordedHead, err := vcs.IsWorktreeSafeToReset(wt.Path, base)
	return err == nil && safe && recordedHead == head
}

// resolveBaseBranch picks the branch worktrees are cut from and reset to: the
// explicitly requested one, otherwise the inferred default.
//
// Only an explicit request is verified. GetDefaultBranch already errors when it
// cannot answer, but an unverified explicit branch would not surface at all:
// acquire SKIPS a slot whose safety check fails, so a typo would look like a
// pool with nothing reusable and burn a fresh slot per call.
func resolveBaseBranch(repoRoot, requested string) (string, error) {
	if requested == "" {
		return vcs.GetDefaultBranch(repoRoot)
	}
	if err := vcs.VerifyBaseBranch(repoRoot, requested); err != nil {
		return "", err
	}
	return requested, nil
}

// markAcquired stamps an acquired worktree entry: a durable lease in lease mode,
// otherwise the default short-lived owner reservation.
func markAcquired(wt *WorktreeEntry, opts acquireOptions) error {
	if opts.lease {
		leaseID, err := newLeaseID()
		if err != nil {
			return err
		}
		wt.Leased = true
		wt.LeaseID = leaseID
		wt.LeaseHolder = opts.leaseHolder
		wt.LeasedAt = time.Now()
		// A lease is process-independent, so it carries no owner reservation.
		wt.OwnerPID = 0
		wt.OwnerStartedAt = 0
		return nil
	}
	return reserveOwner(wt)
}

// ErrLeasePreconditionFailed reports that a conditional release no longer
// identifies the worktree's current lease.
var ErrLeasePreconditionFailed = errors.New("lease precondition failed")

// ReleasePreconditions optionally constrain a release to the current lease.
// Pointer fields distinguish an omitted condition from an expected empty value.
type ReleasePreconditions struct {
	ExpectedLeaseID     *string
	ExpectedLeaseHolder *string
}

// ReleaseOptions configures optional release behavior.
type ReleaseOptions struct {
	Submodules bool
}

// Release resets a managed worktree, clears its short-lived owner reservation or
// durable lease, and returns it to the available pool. It retains the legacy
// unconditional behavior of releasing by path.
func Release(poolDir, worktreePath string, opts ReleaseOptions) error {
	return ReleaseConditional(poolDir, worktreePath, "", opts, ReleasePreconditions{}, nil)
}

// ValidateReleasePreconditions checks that a managed worktree still matches
// the requested lease without performing any release effects.
func ValidateReleasePreconditions(poolDir, worktreePath string, preconditions ReleasePreconditions) error {
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}
		_, err = releasableWorktree(&state, worktreePath, preconditions)
		return err
	})
}

// ReleaseConditional verifies any lease preconditions, runs beforeReset, resets
// the worktree, and clears its reservation while holding one state lock. The
// callback is invoked only after all preconditions match and runs under that
// lock so caller-side termination or detachment cannot race a later acquisition.
// A markerless slot (its .git/.jj marker is gone) is never reset or asked for a
// branch: dispatch on such a path falls back to the configured backend, which
// in an in-project pool resolves the repository ENCLOSING the pool. Its
// reservation is still cleared so the slot is not stuck leased, and the damaged
// slot is left for destroy; acquire refuses to reuse it.
//
// baseBranch parks the returned slot on the branch the pool cuts from; empty
// falls back to the base the slot was acquired with, then to the inferred
// default. Parking is what keeps the slot reusable: acquire recycles only when
// HEAD is merged into the base it resets to, so a slot parked off-base is never
// recycled and every acquire grows the pool until max_trees.
func ReleaseConditional(poolDir, worktreePath, baseBranch string, opts ReleaseOptions, preconditions ReleasePreconditions, beforeReset func() error) error {
	markerless := vcs.WorktreeBackendName(worktreePath) == ""
	// Resolved before the state lock so a failure surfaces before beforeReset
	// kills the worktree's processes. It is only fatal when the slot has no
	// base of its own to park on instead.
	defaultBranch, defaultErr := "", error(nil)
	if !markerless {
		defaultBranch, defaultErr = vcs.DefaultBranchForWorktree(worktreePath)
	}
	return WithStateLock(poolDir, func() error {
		state, err := ReadState(poolDir)
		if err != nil {
			return err
		}

		wt, err := releasableWorktree(&state, worktreePath, preconditions)
		if err != nil {
			return err
		}
		branch, fallback, requested := "", "", ""
		if !markerless {
			requested = baseBranch
			if requested == "" {
				requested = wt.BaseBranch
			}
			if defaultErr != nil && requested == "" {
				return defaultErr
			}
			branch, fallback = defaultBranch, defaultBranch
			if requested != "" {
				branch = requested
			}
		}
		if beforeReset != nil {
			if err := beforeReset(); err != nil {
				return err
			}
		}
		if opts.Submodules {
			if err := releaseSubmodulesLocked(&state, worktreePath); err != nil {
				return err
			}
		}
		if !markerless {
			if err := vcs.ResetWorktree(worktreePath, branch); err != nil {
				// The base resolved when the caller checked it but not now (it
				// was deleted in between). Park on the default rather than
				// strand the reservation with the processes already killed.
				if fallback == "" || fallback == branch {
					return err
				}
				fmt.Fprintf(os.Stderr, "🌳 Warning: cannot park the worktree on %q (%v); using %s instead.\n", branch, err, fallback)
				if err := vcs.ResetWorktree(worktreePath, fallback); err != nil {
					return err
				}
				branch = fallback
				requested = ""
			}
			wt.BaseBranch = requested
		}

		wt.OwnerPID = 0
		wt.OwnerStartedAt = 0
		clearLease(wt)
		return WriteState(poolDir, state)
	})
}

func releasableWorktree(state *State, worktreePath string, preconditions ReleasePreconditions) (*WorktreeEntry, error) {
	for i := range state.Worktrees {
		wt := &state.Worktrees[i]
		if filepath.Clean(wt.Path) != filepath.Clean(worktreePath) {
			continue
		}
		if wt.Destroying {
			return nil, fmt.Errorf("worktree %s is being destroyed", worktreePath)
		}
		if err := validateReleasePreconditions(*wt, preconditions); err != nil {
			return nil, err
		}
		return wt, nil
	}
	return nil, fmt.Errorf("worktree %s is not managed by treehouse", worktreePath)
}

func validateReleasePreconditions(wt WorktreeEntry, preconditions ReleasePreconditions) error {
	if preconditions.ExpectedLeaseID == nil && preconditions.ExpectedLeaseHolder == nil {
		return nil
	}
	if !wt.Leased {
		return fmt.Errorf("%w: worktree %s is not leased", ErrLeasePreconditionFailed, wt.Path)
	}
	if preconditions.ExpectedLeaseID != nil && wt.LeaseID != *preconditions.ExpectedLeaseID {
		return fmt.Errorf("%w: lease identity does not match worktree %s", ErrLeasePreconditionFailed, wt.Path)
	}
	if preconditions.ExpectedLeaseHolder != nil && wt.LeaseHolder != *preconditions.ExpectedLeaseHolder {
		return fmt.Errorf("%w: lease holder does not match worktree %s", ErrLeasePreconditionFailed, wt.Path)
	}
	return nil
}

// List returns the current status of managed worktrees in poolDir.
// Leased worktrees are reported with StatusLeased and their optional holder.
// An idle slot whose .git/.jj marker is gone is reported StatusDamaged: its
// dirtiness is never read, because dispatch on a markerless path falls back to
// the configured backend, which in an in-project pool answers with the facts
// of the repository ENCLOSING the pool.
// ListOptions configures optional list behavior.
type ListOptions struct {
	IncludeSubmodules bool
}

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
				Flavor: vcs.WorktreeBackendName(wt.Path),
			}

			procs, _ := process.FindProcessesInWorktree(wt.Path)
			ws.Processes = procs

			parentActive := false
			if wt.Leased {
				ws.Status = StatusLeased
				ws.LeaseID = wt.LeaseID
				ws.LeaseHolder = wt.LeaseHolder
				ws.LeasedAt = wt.LeasedAt
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
			} else if ws.Flavor == "" {
				ws.Status = StatusDamaged
			} else {
				dirty, _ := rootWorktreeDirty(wt, state)
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
	wt.LeaseID = ""
	wt.LeaseHolder = ""
	wt.LeasedAt = time.Time{}
}

func sameDestroyReservation(current, reserved WorktreeEntry) bool {
	return current.Path == reserved.Path &&
		current.Destroying &&
		current.OwnerPID == reserved.OwnerPID &&
		current.OwnerStartedAt == reserved.OwnerStartedAt
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

// RootDirtyForPool reports whether a superproject worktree should block return
// or reuse because of tracked changes. Untracked content in managed submodules
// is ignored.
func RootDirtyForPool(poolDir, parentPath string) (bool, error) {
	state, err := ReadState(poolDir)
	if err != nil {
		return false, err
	}
	if len(ChildrenOf(state, parentPath)) == 0 {
		return vcs.IsDirty(parentPath)
	}
	return isRootDirtyForPool(parentPath, state)
}

// rootWorktreeDirty reports whether a managed root worktree should block pool
// reuse, prune, or destroy. Managed submodule paths with only untracked
// content do not count as dirty on the parent.
func rootWorktreeDirty(wt WorktreeEntry, state State) (bool, error) {
	if wt.IsRoot() && len(ChildrenOf(state, wt.Path)) > 0 {
		return isRootDirtyForPool(wt.Path, state)
	}
	return vcs.IsDirty(wt.Path)
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
		handled := false
		for subPath, child := range childByPath {
			if path == subPath || strings.HasPrefix(path, subPath+string(filepath.Separator)) {
				handled = true
				dirty, err := git.HasTrackedChanges(child.Path)
				if err != nil {
					return true, err
				}
				if dirty {
					return true, nil
				}
				break
			}
		}
		if handled {
			continue
		}
		return true, nil
	}
	return false, nil
}

