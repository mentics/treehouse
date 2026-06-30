# Submodule Worktree Pooling Design

## Goal

Treehouse should support repositories that contain Git submodules without making
each acquired workspace clone those submodules from scratch. A workspace acquired
from Treehouse should get the same core benefit for submodules that it already
gets for the superproject: a reusable, pre-warmed working tree with dependencies,
build outputs, editor indexes, and other local caches already present.

The important requirement is not only "avoid repeated network clones." It is
"reuse the same checked-out submodule working directories across agent sessions."
If a submodule contains a large dependency install or build cache, that cache
must survive `treehouse return` and be ready for the next `treehouse get`.

## Current Behavior

Today Treehouse manages one pool per repository. The CLI resolves the current Git
repository root, derives one pool directory, and asks `internal/pool` to acquire
one worktree. A managed worktree is created at:

```text
<pool-root>/<repo-name>-<repo-hash>/<slot>/<repo-name>
```

Submodules are not initialized, updated, reset, returned, pruned, or destroyed as
Treehouse-managed entities. Any submodule behavior comes from Git's default
handling of nested repositories.

## Recommended Approach

Add a workspace orchestration layer above the existing per-repository pool.
The orchestration layer acquires the superproject worktree and then reconciles
its submodules as Treehouse-managed child worktrees.

The design has two complementary pieces:

1. Reuse each submodule's existing git directory from the **source superproject
   checkout** (the repository where the user runs `treehouse get`).
2. Slot-affine submodule working trees kept at their real paths inside each
   superproject slot.

This mirrors how Treehouse already handles the superproject: one object store,
many worktrees. Treehouse does **not** bare-clone submodule URLs into a hidden
cache. Submodule paths in pool slots are additional `git worktree add` checkouts
from the same module git directory Git created when the user initialized
submodules in their main checkout.

**Prerequisite:** submodules must be initialized in the source checkout before
`treehouse get --submodules` (for example `git submodule update --init`). If a
submodule is not initialized, Treehouse fails with a clear error instead of
cloning from the network.

## Storage Layout

Keep the existing superproject layout:

```text
~/.treehouse/
  app-a1b2c3/
    treehouse-state.json
    1/
      app/
    2/
      app/
```

Submodule object storage lives in the source repository's normal Git layout, not
under `~/.treehouse/`:

```text
/path/to/your/app/.git/modules/vendor/libfoo/   # backing repo (shared)
/path/to/your/app/vendor/libfoo/                # main checkout worktree
```

Pool slots add more worktrees from that same backing repo:

```text
~/.treehouse/
  app-a1b2c3/
    1/
      app/
        vendor/libfoo/   # worktree of .git/modules/vendor/libfoo
    2/
      app/
        vendor/libfoo/   # separate warm worktree for slot 2
```

This is the critical warm-pool property: when slot `1` is returned, its
`vendor/libfoo` working tree remains in place. The next agent that receives slot
`1` gets the same warm submodule directory, not a newly cloned checkout.

## Lifecycle

### Pool Preparation

Submodule preparation should be part of normal Treehouse pool maintenance, not a
separate user-facing mode. When submodule support is enabled, Treehouse should
keep the parent slots and their child submodule worktrees warm as part of
creating, acquiring, and returning pool entries.

Whenever Treehouse creates or heals a parent slot, it should:

1. Resolve the superproject repository and config.
2. Read `.gitmodules` and the relevant superproject revision to discover
   submodule paths, URLs, and gitlink commits.
3. Resolve each submodule's backing git directory from the **source checkout**
   by following the initialized submodule's `.git` file
   (`git rev-parse --git-common-dir` from `sourceRepo/<path>`).
4. Create a child worktree at the submodule path inside that parent slot if it
   does not already exist (`git worktree add` from the backing repo).
5. Check each child worktree out to the gitlink commit recorded by the
   superproject.
6. Run configured post-create hooks for newly created child worktrees.

The first `treehouse get --submodules` for a cold pool may still need to create
missing **slot** child worktrees. That is local `git worktree add` work only; it
does not clone submodule repositories from the network. After that initial fill,
normal acquire should reuse the warm slot-local submodule directories and only
perform cheap checkout reconciliation.

### Acquire

Add:

```sh
treehouse get --submodules
treehouse get --submodules=recursive
treehouse get --lease --submodules
```

Acquire should:

1. Acquire the superproject worktree using the existing pool logic.
2. Discover submodules from the acquired superproject revision.
3. For each submodule path, find or create the slot-affine child worktree at the
   path inside the acquired parent slot.
4. Reset that child worktree to the exact gitlink commit recorded in the
   superproject index.
5. Record child worktrees as held by the parent worktree while the parent is
   checked out.

Submodules must be reset to gitlink commits, not to their default branches.
Resetting a submodule to its default branch would make the superproject appear
dirty immediately because the submodule HEAD would not match the commit recorded
by the parent repository.

### Return

Return should process children before the parent:

1. Discover the child worktrees associated with the parent worktree.
2. Refuse or prompt if any child submodule worktree is dirty.
3. Terminate lingering processes inside child worktrees.
4. Reset each child to the parent's recorded gitlink commit.
5. Clear the child's active reservation while leaving the child worktree in
   place.
6. Reset and release the parent worktree.

The child worktree directories should not be deleted on normal return. They are
the warm state.

### Status

`treehouse status --submodules` should show nested state:

```text
1     in-use      ~/.treehouse/app-a1b2c3/1/app
      submodule   vendor/libfoo  clean   abc1234
      submodule   vendor/libbar  dirty   def5678
2     available   ~/.treehouse/app-a1b2c3/2/app
      submodule   vendor/libfoo  warm    abc1234
      submodule   vendor/libbar  warm    def5678
```

Plain `treehouse status` can keep its current output, but it should consider a
dirty managed child submodule when deciding whether the parent slot is reusable.

### Prune And Destroy

Prune and destroy need to understand parent-child ownership:

- A parent worktree with dirty or in-use child worktrees is not disposable.
- A child worktree is not independently pruned while its parent slot exists and
  the submodule is still configured.
- If a submodule is removed from the superproject, Treehouse may prune that
  child worktree only after the usual clean, idle, merged, and unleased checks.
- Destroying a parent worktree should either destroy its managed child worktrees
  first or refuse unless a recursive/include flag is supplied.

The normal `treehouse return` path should preserve child worktrees. Prune and
destroy are the only lifecycle operations that should remove them.

## State Model

The existing `WorktreeEntry` is sufficient for a single flat repository pool,
but submodules need explicit metadata. A practical extension is:

```go
type WorktreeKind string

const (
    WorktreeKindRoot      WorktreeKind = "root"
    WorktreeKindSubmodule WorktreeKind = "submodule"
)

type WorktreeEntry struct {
    Name      string
    Path      string
    CreatedAt time.Time

    Kind WorktreeKind `json:"kind,omitempty"`

    // For submodule worktrees.
    ParentPath      string `json:"parent_path,omitempty"`
    SubmodulePath   string `json:"submodule_path,omitempty"`
    SubmoduleURL    string `json:"submodule_url,omitempty"`
    BackingRepoPath string `json:"backing_repo_path,omitempty"`
    ExpectedCommit  string `json:"expected_commit,omitempty"`

    // Existing lifecycle fields remain.
    Destroying     bool
    OwnerPID       int32
    OwnerStartedAt int64
    Leased         bool
    LeaseHolder    string
    LeasedAt       time.Time
}
```

The child entries can live in the parent pool state because they are slot-affine
to the parent worktree path. `BackingRepoPath` records the resolved module git
directory from the source checkout (typically under `.git/modules/...`).

## Git Operations

Git helpers used by the orchestration layer:

- `ListSubmodules(repoRoot string) ([]Submodule, error)`
- `SubmoduleGitlinkCommit(repoRoot, path string) (string, error)`
- `ResolveSubmoduleRepoDir(sourceRepoRoot, submodulePath string) (string, error)`
- `FetchRepo(repoPath string) error`
- `AddWorktreeAtRef(repoPath, worktreePath, ref string) error`
- `ResetWorktreeToRef(worktreePath, ref string) error`

Implementation notes:

- Use `git config --file .gitmodules --get-regexp` only through structured helper
  functions.
- Prefer absolute paths internally and `filepath` helpers for Windows support.
- Do not rely on Unix symlinks or bind mounts. They are not portable and can
  confuse Git's submodule status behavior.
- Avoid `git submodule update --init` on **pool slot paths** (the managed path
  inside `~/.treehouse/...`). That would clone into the slot. Initialization
  belongs in the user's main checkout before `treehouse get --submodules`.
- On prune/destroy, remove only slot worktree registrations. Never delete the
  shared module git directory under `.git/modules/`.

## Handling Changed Submodule Sets

When a superproject revision adds a new submodule, acquire should create or
prepare the missing child worktree. If this happens during normal `get`, print a
clear message that one-time submodule pool setup is being performed.

When a superproject revision removes a submodule, Treehouse should not leave the
old child directory as an untracked nested repository under the parent. Options:

1. If the child is clean and idle, remove it with `git worktree remove` and clear
   its state entry.
2. If the child is dirty, leased, or in use, keep the parent slot unavailable and
   report the child as blocking cleanup.

This preserves safety over convenience. A removed submodule with local work is
user data and should not be silently deleted.

## Recursive Submodules

Recursive submodules can use the same model. Each submodule worktree can have
its own children, and those children are slot-affine to their immediate parent
path.

For a first implementation, support `--submodules=top` by default and make
`--submodules=recursive` opt-in. Recursive mode needs careful cycle detection,
depth limits, and clear status output.

## Configuration

Suggested config:

```toml
[submodules]
enabled = false
mode = "top"          # "top" or "recursive"
fetch = "on-acquire"  # "always", "on-acquire", or "never"
```

Defaulting `enabled` to false keeps current users' behavior unchanged. Users who
depend on submodules can opt in explicitly.

## Failure Behavior

Submodule reconciliation should fail closed:

- If a submodule is not initialized in the source checkout, fail with an
  actionable error (`git submodule update --init <path>`).
- If a child worktree is dirty, do not hand the parent slot out as clean.
- If the expected gitlink commit is missing from the submodule backing repo,
  fetch if allowed; otherwise mark the workspace unavailable with a clear error.
- If a process is running inside a child worktree, treat the parent as in use.
- If child cleanup fails, leave the parent reserved or dirty rather than
  returning a half-clean workspace to the pool.

This matches Treehouse's existing bias toward preserving local work and avoiding
surprising deletes.

## Implementation Plan

1. Add git helpers for submodule discovery, gitlink commit lookup, cached repo
   creation, and checkout-to-ref.
2. Add a small `internal/submodulepool` package that orchestrates child
   worktrees using the existing `internal/pool` safety concepts.
3. Extend state to record child worktrees and parent-child relationships.
4. Add submodule-aware pool preparation when parent slots are created, acquired,
   returned, or healed.
5. Add `treehouse get --submodules` and route acquire through the workspace
   orchestration layer.
6. Update `return`, `status`, `prune`, and `destroy` to account for managed
   child worktrees.
7. Add end-to-end tests with a superproject and local submodule remotes,
   including dirty child protection and a second acquire proving the child
   worktree path and cache contents were reused.

## Test Cases

Minimum test coverage should include:

- Submodule-aware pool preparation resolves backing repos from the source
  checkout and creates slot-affine child worktrees (no hidden bare clone cache).
- A second acquire reuses the same submodule working directory and preserves an
  untracked cache file that is intentionally ignored by the submodule.
- Acquire without initialized submodules in the source checkout fails clearly.
- Acquire checks out the submodule to the parent gitlink commit, not the
  submodule default branch.
- Dirty child submodules prevent parent return or reuse unless the user confirms
  cleanup.
- Processes inside child submodules make the parent slot in-use.
- Removed submodules do not leave untracked nested repositories that make the
  parent appear dirty.
- Global prune does not delete active child worktrees independently of their
  parent slots.
- The behavior works on Linux, macOS, and Windows without symlink or bind mount
  assumptions.

## Open Questions

- Should submodule pooling be enabled only by config, only by CLI flag, or both?
- Should post-create hooks run for every child worktree, or should there be
  separate submodule-specific hooks?
- Should Treehouse proactively fill all available parent slots when submodules
  are enabled, or only prepare slots as they are first created/acquired?

The recommended default is explicit opt-in with automatic pool preparation. That
keeps the user-facing model simple: initialize submodules in your main checkout,
enable submodules, then use `treehouse get` as usual while normal acquire/return
cycles reuse warm submodule directories.
