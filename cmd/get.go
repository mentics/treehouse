package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/process"
	"github.com/kunchenguid/treehouse/internal/shell"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var (
	getLease          bool
	getLeaseHolder    string
	getSubmodules     bool
	getSubmodulesMode string
)

var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Acquire a worktree from the pool and open a subshell",
	Long: `Acquire a worktree from the pool and open a subshell in it.

Pass --lease for a non-interactive, durable acquire: treehouse reserves the
worktree, marks it leased in its persistent state, and prints only the worktree's
absolute path to stdout (all banners go to stderr). A leased worktree is never
handed out by a later get and never removed by prune, even with no process
running inside it, until you release it with 'treehouse return <path>'.

Pass --submodules to prepare managed submodule worktrees at their real paths
inside the acquired slot. Submodule pooling can also be enabled in treehouse.toml.`,
	RunE: getRunE,
}

func init() {
	getCmd.Flags().BoolVar(&getLease, "lease", false, "Durably lease a worktree without opening a subshell; print only its path to stdout")
	getCmd.Flags().StringVar(&getLeaseHolder, "lease-holder", "", "Optional label recorded as the lease holder (defaults to $TREEHOUSE_LEASE_HOLDER)")
	getCmd.Flags().BoolVar(&getSubmodules, "submodules", false, "Prepare managed submodule worktrees for the acquired slot")
	getCmd.Flags().StringVar(&getSubmodulesMode, "submodules-mode", "", "Submodule depth: top (default) or recursive (not supported yet)")
	rootCmd.AddCommand(getCmd)
}

func getRunE(cmd *cobra.Command, args []string) error {
	repoRoot, err := git.FindRepoRoot()
	if err != nil {
		return fmt.Errorf("not in a git repository: %w", err)
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if err := resolveGetSubmodulesMode(cfg); err != nil {
		return err
	}

	poolDir, err := config.ResolvePoolDir(repoRoot, cfg.Root)
	if err != nil {
		return fmt.Errorf("failed to resolve pool directory: %w", err)
	}

	if err := config.EnsureGitignore(filepath.Dir(poolDir)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to update .gitignore: %v\n", err)
	}

	acquireOpts := buildAcquireOptions(cfg)

	if getLease {
		return getLeaseRunE(repoRoot, poolDir, cfg, acquireOpts)
	}

	wtPath, err := pool.Acquire(repoRoot, poolDir, cfg.MaxTrees, cfg.Hooks.PostCreate, acquireOpts)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "🌳 Entered worktree at %s. Type 'exit' to return.\n", ui.PrettyPath(wtPath))

	env := []string{
		"TREEHOUSE_DIR=" + wtPath,
	}
	_, err = shell.Spawn(wtPath, env)

	// Subshell exited — handle return
	if err := git.DetachWorktree(wtPath); err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to detach worktree HEAD: %v\n", err)
	}

	if err := confirmAndReturnWorktree(poolDir, wtPath, acquireOpts); err != nil {
		return err
	}

	return nil
}

func resolveGetSubmodulesMode(cfg config.Config) error {
	mode := cfg.Submodules.Mode
	if getSubmodulesMode != "" {
		mode = getSubmodulesMode
	}
	return config.ValidateSubmodulesMode(mode)
}

func buildAcquireOptions(cfg config.Config) pool.AcquireOptions {
	active := config.SubmodulesActive(cfg, getSubmodules)
	subCfg := cfg.Submodules
	if getSubmodulesMode != "" {
		subCfg.Mode = getSubmodulesMode
	}
	return pool.AcquireOptions{
		Submodules:    active,
		SubmodulesCfg: subCfg,
	}
}

func buildReleaseOptions(cfg config.Config) pool.ReleaseOptions {
	return pool.ReleaseOptions{
		Submodules: config.SubmodulesActive(cfg, getSubmodules),
	}
}

func confirmAndReturnWorktree(poolDir, wtPath string, acquireOpts pool.AcquireOptions) error {
	dirty, _ := git.IsDirty(wtPath)
	if dirty {
		fmt.Fprintf(os.Stderr, "🌳 Worktree has uncommitted changes.\n")
		ok, promptErr := ui.Confirm("Clean worktree and return to pool?", true)
		if promptErr != nil || !ok {
			fmt.Fprintln(os.Stderr, "🌳 Worktree left dirty. Use 'treehouse return --force' to clean it later.")
			return nil
		}
	}

	if acquireOpts.Submodules {
		state, err := pool.ReadState(poolDir)
		if err != nil {
			return err
		}
		for _, subPath := range pool.DirtySubmodules(state, wtPath) {
			fmt.Fprintf(os.Stderr, "🌳 Submodule %s has uncommitted changes.\n", subPath)
			ok, promptErr := ui.Confirm("Clean submodule and return to pool?", true)
			if promptErr != nil || !ok {
				fmt.Fprintln(os.Stderr, "🌳 Worktree left dirty. Use 'treehouse return --force' to clean it later.")
				return nil
			}
		}
	}

	killLingeringProcesses(wtPath)

	releaseOpts := pool.ReleaseOptions{Submodules: acquireOpts.Submodules}
	if err := pool.Release(poolDir, wtPath, releaseOpts); err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to clean worktree: %v\n", err)
	} else {
		fmt.Fprintln(os.Stderr, "🌳 Worktree returned to pool.")
	}
	return nil
}

// getLeaseRunE performs a non-interactive, durable acquire. It reserves a
// worktree, marks it leased in persistent state, prints only the worktree path
// to stdout, and routes every human-facing message to stderr so that
// `path=$(treehouse get --lease)` works cleanly in scripts.
func getLeaseRunE(repoRoot, poolDir string, cfg config.Config, acquireOpts pool.AcquireOptions) error {
	holder := getLeaseHolder
	if holder == "" {
		holder = os.Getenv("TREEHOUSE_LEASE_HOLDER")
	}

	wtPath, err := pool.AcquireLease(repoRoot, poolDir, cfg.MaxTrees, cfg.Hooks.PostCreate, holder, acquireOpts)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "🌳 Leased worktree at %s. Run 'treehouse return %s' to release it.\n",
		ui.PrettyPath(wtPath), ui.PrettyPath(wtPath))
	fmt.Fprintln(os.Stdout, wtPath)
	return nil
}

// killLingeringProcesses terminates any process whose cwd is within the given
// worktree. Called before returning a worktree to the pool so detached tools
// (e.g. opencode servers that ignore SIGHUP) don't keep holding the worktree.
func killLingeringProcesses(wtPath string) {
	killed, err := process.TerminateWorktreeProcesses(wtPath, 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "🌳 Warning: failed to scan for lingering processes: %v\n", err)
		return
	}
	if len(killed) == 0 {
		return
	}
	names := make([]string, len(killed))
	for i, p := range killed {
		names[i] = p.String()
	}
	fmt.Fprintf(os.Stderr, "🌳 Terminated lingering processes: %s\n", strings.Join(names, ", "))
}
