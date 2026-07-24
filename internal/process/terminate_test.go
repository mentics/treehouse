package process

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestFilterProtectedProcesses_SkipsCurrentProcessAndAncestors(t *testing.T) {
	wt := filepath.FromSlash("/tmp/wt")
	procs := []ProcessInfo{
		{PID: 100, Name: "shell"},
		{PID: 200, Name: "treehouse"},
		{PID: 300, Name: "server"},
	}

	// Orphan leftover (ppid 1) — eligible to kill once Treehouse chain is skipped.
	parents := map[int32]int32{
		200: 100,
		100: 1,
		1:   0,
		300: 1,
	}
	cwds := map[int32]string{
		100: filepath.FromSlash("/tmp/elsewhere"),
		300: wt,
	}

	filtered := filterProtectedProcesses(procs, wt, 200, parentMap(parents), cwdMap(cwds))

	if len(filtered) != 1 {
		t.Fatalf("expected 1 process after filtering, got %+v", filtered)
	}
	if filtered[0].PID != 300 {
		t.Fatalf("expected pid 300 to remain, got %d", filtered[0].PID)
	}
}

func TestFilterProtectedProcesses_SkipsTerminationWhenParentLookupFails(t *testing.T) {
	wt := filepath.FromSlash("/tmp/wt")
	procs := []ProcessInfo{
		{PID: 100, Name: "shell"},
		{PID: 200, Name: "treehouse"},
		{PID: 300, Name: "server"},
	}

	filtered := filterProtectedProcesses(procs, wt, 200, func(pid int32) (int32, error) {
		if pid == 200 {
			return 0, errors.New("cannot inspect parent")
		}
		return 0, nil
	}, cwdMap(nil))

	if len(filtered) != 0 {
		t.Fatalf("expected no processes after filtering, got %+v", filtered)
	}
}

func TestFilterProtectedProcesses_SkipsOutsideRootedSession(t *testing.T) {
	wt := filepath.FromSlash("/tmp/wt")
	outside := filepath.FromSlash("/home/user")

	procs := []ProcessInfo{
		{PID: 60, Name: "node"},     // cwd in worktree, parent outside → spare
		{PID: 65, Name: "bash"},     // child of node, still outside-rooted → spare
		{PID: 70, Name: "opencode"}, // orphaned leftover → kill
		{PID: 200, Name: "treehouse"},
	}

	parents := map[int32]int32{
		200: 1,
		60:  50, // cursor-server outside candidates
		65:  60,
		70:  1,
		50:  1,
		1:   0,
	}
	cwds := map[int32]string{
		50: outside,
		60: wt,
		65: wt,
		70: wt,
	}

	filtered := filterProtectedProcesses(procs, wt, 200, parentMap(parents), cwdMap(cwds))

	if len(filtered) != 1 {
		t.Fatalf("expected 1 process after filtering, got %+v", filtered)
	}
	if filtered[0].PID != 70 || filtered[0].Name != "opencode" {
		t.Fatalf("expected opencode (70) to remain, got %+v", filtered[0])
	}
}

func TestFilterProtectedProcesses_KillsWorktreeRootedTree(t *testing.T) {
	wt := filepath.FromSlash("/tmp/wt")

	procs := []ProcessInfo{
		{PID: 80, Name: "shell"},
		{PID: 81, Name: "tool"},
		{PID: 200, Name: "treehouse"},
	}

	// Fully worktree-rooted: shell orphaned to init, tool child of shell.
	parents := map[int32]int32{
		200: 1,
		80:  1,
		81:  80,
		1:   0,
	}
	cwds := map[int32]string{
		80: wt,
		81: filepath.Join(wt, "src"),
	}

	filtered := filterProtectedProcesses(procs, wt, 200, parentMap(parents), cwdMap(cwds))

	if len(filtered) != 2 {
		t.Fatalf("expected shell and tool to remain killable, got %+v", filtered)
	}
}

func TestFilterProtectedProcesses_UnreadableAncestorCwdIsProtected(t *testing.T) {
	wt := filepath.FromSlash("/tmp/wt")

	procs := []ProcessInfo{
		{PID: 60, Name: "mystery"},
		{PID: 70, Name: "opencode"},
		{PID: 200, Name: "treehouse"},
	}

	parents := map[int32]int32{
		200: 1,
		60:  50,
		70:  1,
		1:   0,
	}
	cwds := map[int32]string{
		70: wt,
		// 50 missing → lookup error → protect 60
	}

	filtered := filterProtectedProcesses(procs, wt, 200, parentMap(parents), cwdMap(cwds))

	if len(filtered) != 1 || filtered[0].PID != 70 {
		t.Fatalf("expected only orphaned opencode, got %+v", filtered)
	}
}

func TestHasOutsideCwdAncestor(t *testing.T) {
	wt := filepath.FromSlash("/tmp/wt")
	outside := filepath.FromSlash("/var/elsewhere")

	parents := map[int32]int32{
		10: 11,
		11: 12,
		12: 1,
		20: 1,
		30: 31,
		31: 1,
		1:  0,
	}
	cwds := map[int32]string{
		11: wt,
		12: outside,
		31: wt,
	}

	if !hasOutsideCwdAncestor(10, wt, parentMap(parents), cwdMap(cwds)) {
		t.Fatal("expected outside ancestor for pid 10")
	}
	if hasOutsideCwdAncestor(20, wt, parentMap(parents), cwdMap(cwds)) {
		t.Fatal("orphaned pid 20 should have no outside ancestor")
	}
	if hasOutsideCwdAncestor(30, wt, parentMap(parents), cwdMap(cwds)) {
		t.Fatal("worktree-rooted pid 30 should have no outside ancestor")
	}
}

func TestPathInWorktree(t *testing.T) {
	wt := filepath.FromSlash("/tmp/wt")
	if !pathInWorktree(wt, wt) {
		t.Fatal("worktree root should be inside itself")
	}
	if !pathInWorktree(wt, filepath.Join(wt, "src")) {
		t.Fatal("descendant should be inside")
	}
	if pathInWorktree(wt, filepath.FromSlash("/tmp")) {
		t.Fatal("parent dir should be outside")
	}
	if pathInWorktree(wt, filepath.FromSlash("/tmp/wt-other")) {
		t.Fatal("sibling prefix should be outside")
	}
}

func parentMap(m map[int32]int32) func(int32) (int32, error) {
	return func(pid int32) (int32, error) {
		parent, ok := m[pid]
		if !ok {
			return 0, errors.New("unknown pid")
		}
		return parent, nil
	}
}

func cwdMap(m map[int32]string) func(int32) (string, error) {
	return func(pid int32) (string, error) {
		cwd, ok := m[pid]
		if !ok {
			return "", errors.New("unknown cwd")
		}
		return cwd, nil
	}
}
