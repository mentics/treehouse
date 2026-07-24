package process

import (
	"os"
	"time"

	gopsutilprocess "github.com/shirou/gopsutil/v4/process"
)

// TerminateWorktreeProcesses finds every process whose cwd is within the given
// worktree path and terminates detached leftovers.
//
// A cwd match is skipped when Treehouse itself (or an ancestor) owns it, or when
// any living ancestor has a cwd outside the worktree. That spares active
// sessions rooted elsewhere (editors, remote agents, parent shells) without
// hardcoding process names. Orphaned or fully worktree-rooted trees are still
// terminated.
//
// On unix it sends SIGTERM, waits up to gracePeriod for processes to exit,
// then SIGKILLs any survivors. On windows it uses TerminateProcess.
//
// Returns the list of processes that were targeted. Errors only if the initial
// scan fails; individual kill failures (e.g. process already gone) are
// swallowed.
// terminateEnabled gates worktree process kills. Set false to diagnose whether
// termination restarts remote/devcontainer sessions on return.
const terminateEnabled = false

// TerminateWorktreeProcesses finds every process whose cwd is within the given
// worktree path and terminates detached leftovers.
//
// A cwd match is skipped when Treehouse itself (or an ancestor) owns it, or when
// any living ancestor has a cwd outside the worktree. That spares active
// sessions rooted elsewhere (editors, remote agents, parent shells) without
// hardcoding process names. Orphaned or fully worktree-rooted trees are still
// terminated.
//
// On unix it sends SIGTERM, waits up to gracePeriod for processes to exit,
// then SIGKILLs any survivors. On windows it uses TerminateProcess.
//
// Returns the list of processes that were targeted. Errors only if the initial
// scan fails; individual kill failures (e.g. process already gone) are
// swallowed.
func TerminateWorktreeProcesses(worktreePath string, gracePeriod time.Duration) ([]ProcessInfo, error) {
	if !terminateEnabled {
		return nil, nil
	}

	absWorktree, err := absResolved(worktreePath)
	if err != nil {
		return nil, err
	}

	procs, err := FindProcessesInWorktree(absWorktree)
	if err != nil {
		return nil, err
	}
	procs = filterProtectedProcesses(procs, absWorktree, int32(os.Getpid()), parentPID, processCwd)
	if len(procs) == 0 {
		return nil, nil
	}

	pids := make([]int32, len(procs))
	for i, p := range procs {
		pids[i] = p.PID
	}

	terminate(pids, gracePeriod)
	return procs, nil
}

func filterProtectedProcesses(
	procs []ProcessInfo,
	absWorktree string,
	currentPID int32,
	lookupParent func(int32) (int32, error),
	lookupCwd func(int32) (string, error),
) []ProcessInfo {
	protected := map[int32]struct{}{
		currentPID: {},
	}

	if !addAncestorChain(protected, currentPID, lookupParent) {
		return nil
	}

	for _, proc := range procs {
		if _, skip := protected[proc.PID]; skip {
			continue
		}
		if hasOutsideCwdAncestor(proc.PID, absWorktree, lookupParent, lookupCwd) {
			protected[proc.PID] = struct{}{}
		}
	}

	filtered := procs[:0]
	for _, proc := range procs {
		if _, skip := protected[proc.PID]; skip {
			continue
		}
		filtered = append(filtered, proc)
	}
	return filtered
}

// hasOutsideCwdAncestor reports whether pid has a living ancestor whose cwd is
// outside absWorktree. Unreadable parent or cwd is treated as outside so we do
// not terminate into an unknown session tree. Ancestors with pid <= 1 end the
// walk (orphaned / reached init) without counting as outside.
func hasOutsideCwdAncestor(
	pid int32,
	absWorktree string,
	lookupParent func(int32) (int32, error),
	lookupCwd func(int32) (string, error),
) bool {
	seen := map[int32]struct{}{pid: {}}
	for {
		parent, err := lookupParent(pid)
		if err != nil {
			return true
		}
		if parent <= 1 {
			return false
		}
		if _, ok := seen[parent]; ok {
			return true
		}
		seen[parent] = struct{}{}

		cwd, err := lookupCwd(parent)
		if err != nil {
			return true
		}
		if !pathInWorktree(absWorktree, cwd) {
			return true
		}
		pid = parent
	}
}

// addAncestorChain walks parents of start into protected. A lookup error
// returns false so the caller can skip all termination (fail closed).
func addAncestorChain(protected map[int32]struct{}, start int32, lookupParent func(int32) (int32, error)) bool {
	for pid := start; pid > 0; {
		parent, err := lookupParent(pid)
		if err != nil {
			return false
		}
		if parent <= 0 {
			break
		}
		if _, seen := protected[parent]; seen {
			break
		}
		protected[parent] = struct{}{}
		pid = parent
	}
	return true
}

func processCwd(pid int32) (string, error) {
	proc, err := gopsutilprocess.NewProcess(pid)
	if err != nil {
		return "", err
	}
	cwd, err := proc.Cwd()
	if err != nil {
		return "", err
	}
	return absResolved(cwd)
}

func parentPID(pid int32) (int32, error) {
	proc, err := gopsutilprocess.NewProcess(pid)
	if err != nil {
		return 0, err
	}
	return proc.Ppid()
}
