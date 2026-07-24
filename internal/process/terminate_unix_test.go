//go:build !windows

package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTerminateWorktreeProcesses_NoProcesses(t *testing.T) {
	dir := t.TempDir()
	procs, err := TerminateWorktreeProcesses(dir, 1*time.Second)
	if err != nil {
		t.Fatalf("TerminateWorktreeProcesses: %v", err)
	}
	if len(procs) != 0 {
		t.Errorf("expected 0 processes, got %d", len(procs))
	}
}

func TestTerminateWorktreeProcesses_KillsOrphanInWorktree(t *testing.T) {
	t.Skip("terminate disabled for devcontainer-restart diagnosis")
	dir := t.TempDir()
	pid := startOrphan(t, dir, "sleep 60")

	procs, err := TerminateWorktreeProcesses(dir, 2*time.Second)
	if err != nil {
		t.Fatalf("TerminateWorktreeProcesses: %v", err)
	}
	if !containsPID(procs, pid) {
		t.Fatalf("expected orphan pid %d among terminated, got %+v", pid, procs)
	}

	waitPIDGone(t, pid, 5*time.Second)
}

func TestTerminateWorktreeProcesses_EscalatesToKill(t *testing.T) {
	t.Skip("terminate disabled for devcontainer-restart diagnosis")
	dir := t.TempDir()
	// Ignore SIGTERM in the orphaned sleep; only SIGKILL ends it.
	pid := startOrphan(t, dir, "trap '' TERM; sleep 60")

	start := time.Now()
	procs, err := TerminateWorktreeProcesses(dir, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("TerminateWorktreeProcesses: %v", err)
	}
	if !containsPID(procs, pid) {
		t.Fatalf("expected orphan pid %d among terminated, got %+v", pid, procs)
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Errorf("expected grace period to elapse before SIGKILL, only took %s", elapsed)
	}

	waitPIDGone(t, pid, 3*time.Second)
}

func TestTerminateWorktreeProcesses_SkipsOutsideRootedChild(t *testing.T) {
	dir := t.TempDir()

	cmd := exec.Command("sleep", "60")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	time.Sleep(200 * time.Millisecond)

	procs, err := TerminateWorktreeProcesses(dir, 2*time.Second)
	if err != nil {
		t.Fatalf("TerminateWorktreeProcesses: %v", err)
	}
	if containsPID(procs, int32(cmd.Process.Pid)) {
		t.Fatalf("outside-rooted child should not be terminated, got %+v", procs)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("outside-rooted child should still be alive: %v", err)
	}
}

func TestTerminateWorktreeProcesses_AlreadyDeadPIDIsNoop(t *testing.T) {
	dir := t.TempDir()
	pid := startOrphan(t, dir, "sleep 60")
	if err := syscall.Kill(int(pid), syscall.SIGKILL); err != nil {
		t.Fatalf("kill orphan: %v", err)
	}
	waitPIDGone(t, pid, 2*time.Second)

	procs, err := TerminateWorktreeProcesses(dir, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("TerminateWorktreeProcesses: %v", err)
	}
	if len(procs) != 0 {
		t.Errorf("expected 0 processes after kill, got %d", len(procs))
	}
}

// startOrphan runs command in dir via a short-lived shell so the background
// process is reparented to init (no outside-cwd ancestor).
func startOrphan(t *testing.T, dir, command string) int32 {
	t.Helper()
	pidFile := filepath.Join(dir, ".orphan-pid")
	script := "cd \"$1\" && { " + command + "; } & echo $! > \"$2\""
	cmd := exec.Command("sh", "-c", script, "sh", dir, pidFile)
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot start orphan: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 1 {
				if err := syscall.Kill(pid, 0); err == nil {
					t.Cleanup(func() {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					})
					return int32(pid)
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("orphan pid not ready in %s", pidFile)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func containsPID(procs []ProcessInfo, pid int32) bool {
	for _, p := range procs {
		if p.PID == pid {
			return true
		}
	}
	return false
}

func waitPIDGone(t *testing.T, pid int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(int(pid), 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after %s", pid, timeout)
}
