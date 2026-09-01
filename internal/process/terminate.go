package process

import (
	"errors"
	"fmt"
	"os"
	"time"

	gopsutilprocess "github.com/shirou/gopsutil/v4/process"
)

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
// On unix it sends SIGTERM and waits up to gracePeriod for processes to exit.
// It then sends SIGKILL to any survivors and waits up to gracePeriod again for
// them to exit. On windows it uses TerminateProcess.
//
// Returns the list of processes that were targeted. It returns an error when
// process discovery or caller ancestry lookup fails. Individual kill failures
// (e.g. a process already gone) are swallowed.
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
	procs, err = filterProtectedProcesses(procs, absWorktree, int32(os.Getpid()), parentPID, processCwd)
	if err != nil {
		return nil, err
	}
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

// UnprotectedProcessesInWorktree returns processes whose cwd is within the
// worktree, excluding the caller and its ancestors. These are exactly the
// processes TerminateWorktreeProcesses would target, so a non-empty result
// after termination means a foreign live writer remains inside the worktree.
// Callers re-scan with it before resetting a slot so a process that started
// during the grace period, or one an ancestry-lookup failure spared, is not
// mistaken for a quiet worktree.
func UnprotectedProcessesInWorktree(worktreePath string) ([]ProcessInfo, error) {
	absWorktree, err := absResolved(worktreePath)
	if err != nil {
		return nil, err
	}
	procs, err := FindProcessesInWorktree(absWorktree)
	if err != nil {
		return nil, err
	}
	return filterProtectedProcesses(procs, absWorktree, int32(os.Getpid()), parentPID, processCwd)
}

// filterProtectedProcesses drops the caller and its ancestors from procs so
// termination never signals the process running return or its parents. A
// failure walking the ancestry is returned as an error rather than swallowed:
// silently protecting every process would let the caller mistake "the filter
// gave up" for "nothing needed killing" and reset the worktree with live
// writers still inside it. The one benign exception is an ancestor that has
// already exited: it ends the walk (everything below it is protected and its
// own ancestors are gone), which is common on Windows where a parent can exit
// and leave a dangling parent PID.
//
// Processes with a living ancestor whose cwd is outside the worktree are also
// protected so devcontainer and remote-agent sessions are not torn down on return.
func filterProtectedProcesses(
	procs []ProcessInfo,
	absWorktree string,
	currentPID int32,
	lookupParent func(int32) (int32, error),
	lookupCwd func(int32) (string, error),
) ([]ProcessInfo, error) {
	callerProtected := map[int32]struct{}{
		currentPID: {},
	}

	for pid := currentPID; pid > 0; {
		parent, err := lookupParent(pid)
		if err != nil {
			if errors.Is(err, gopsutilprocess.ErrorProcessNotRunning) {
				break
			}
			return nil, fmt.Errorf("cannot resolve ancestry of process %d: %w", pid, err)
		}
		if parent <= 0 {
			break
		}
		if _, seen := callerProtected[parent]; seen {
			break
		}
		callerProtected[parent] = struct{}{}
		pid = parent
	}

	spared := map[int32]struct{}{}
	for pid := range callerProtected {
		spared[pid] = struct{}{}
	}
	for _, proc := range procs {
		if _, skip := spared[proc.PID]; skip {
			continue
		}
		if hasOutsideCwdAncestor(proc.PID, absWorktree, callerProtected, lookupParent, lookupCwd) {
			spared[proc.PID] = struct{}{}
		}
	}

	filtered := procs[:0]
	for _, proc := range procs {
		if _, skip := spared[proc.PID]; skip {
			continue
		}
		filtered = append(filtered, proc)
	}
	return filtered, nil
}

// hasOutsideCwdAncestor reports whether pid has a living ancestor whose cwd is
// outside absWorktree without passing through the caller's own ancestor chain.
// Unreadable parent or cwd is treated as outside so we do not terminate into an
// unknown session tree. Ancestors with pid <= 1 end the walk (orphaned / reached
// init) without counting as outside.
func hasOutsideCwdAncestor(
	pid int32,
	absWorktree string,
	callerProtected map[int32]struct{},
	lookupParent func(int32) (int32, error),
	lookupCwd func(int32) (string, error),
) bool {
	seen := map[int32]struct{}{pid: {}}
	for {
		parent, err := lookupParent(pid)
		if err != nil {
			if errors.Is(err, gopsutilprocess.ErrorProcessNotRunning) {
				return false
			}
			return true
		}
		if parent <= 1 {
			return false
		}
		if _, ok := seen[parent]; ok {
			return true
		}
		seen[parent] = struct{}{}
		if _, skip := callerProtected[parent]; skip {
			return false
		}

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
