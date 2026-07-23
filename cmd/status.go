package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
	"github.com/kunchenguid/treehouse/internal/git"
	"github.com/kunchenguid/treehouse/internal/pool"
	"github.com/kunchenguid/treehouse/internal/ui"
)

var (
	statusSubmodules bool
	statusJSON       bool
)

type statusJSONProcess struct {
	PID  int32  `json:"pid"`
	Name string `json:"name"`
}

type statusJSONWorktree struct {
	Name        string              `json:"name"`
	Path        string              `json:"path"`
	Status      string              `json:"status"`
	LeaseID     string              `json:"lease_id"`
	LeaseHolder string              `json:"lease_holder"`
	LeasedAt    *time.Time          `json:"leased_at"`
	Processes   []statusJSONProcess `json:"processes"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the status of all worktrees in the pool",
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := git.FindRepoRoot()
		if err != nil {
			return fmt.Errorf("not in a git repository: %w", err)
		}

		cfg, err := config.Load(repoRoot)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		poolDir, err := config.ResolvePoolDir(repoRoot, cfg.Root)
		if err != nil {
			return err
		}

		includeSubmodules := statusSubmodules || config.SubmodulesActive(cfg, false)
		worktrees, err := pool.List(poolDir, pool.ListOptions{IncludeSubmodules: includeSubmodules})
		if err != nil {
			return err
		}

		if statusJSON {
			return writeStatusJSON(worktrees)
		}

		if len(worktrees) == 0 {
			fmt.Fprintln(os.Stderr, "🌳 No worktrees in pool.")
			return nil
		}

		green := color.New(color.FgGreen).SprintFunc()
		red := color.New(color.FgRed).SprintFunc()
		yellow := color.New(color.FgYellow).SprintFunc()
		cyan := color.New(color.FgCyan, color.Bold).SprintFunc()
		magenta := color.New(color.FgMagenta).SprintFunc()

		const statusWidth = 11

		for _, wt := range worktrees {
			var status string
			switch wt.Status {
			case pool.StatusAvailable:
				status = green(wt.Status)
			case pool.StatusInUse:
				status = red(wt.Status)
			case pool.StatusDirty:
				status = yellow(wt.Status)
			case pool.StatusLeased:
				status = magenta(wt.Status)
			case pool.StatusHere:
				status = cyan(wt.Status)
			}

			statusPad := strings.Repeat(" ", statusWidth-len(wt.Status))
			line := fmt.Sprintf("%-4s  %s%s  %s", wt.Name, status, statusPad, ui.PrettyPath(wt.Path))
			if wt.Status == pool.StatusLeased && wt.LeaseHolder != "" {
				line += fmt.Sprintf("  (held by %s)", wt.LeaseHolder)
			}
			fmt.Fprintln(os.Stdout, line)

			if len(wt.Processes) > 0 {
				var procStrs []string
				for _, p := range wt.Processes {
					procStrs = append(procStrs, p.String())
				}
				fmt.Fprintf(os.Stdout, "%s%s\n", strings.Repeat(" ", 4+2+statusWidth+2), strings.Join(procStrs, ", "))
			}

			if includeSubmodules {
				for _, child := range wt.Children {
					childStatus := child.Status
					switch child.Status {
					case pool.SubmoduleStatusClean, pool.SubmoduleStatusWarm:
						childStatus = green(child.Status)
					case pool.SubmoduleStatusDirty:
						childStatus = yellow(child.Status)
					case pool.SubmoduleStatusInUse, pool.SubmoduleStatusLeased:
						childStatus = red(child.Status)
					}
					commit := child.ExpectedCommit
					if commit == "" {
						commit = child.HeadCommit
					}
					fmt.Fprintf(os.Stdout, "%s      submodule   %-20s  %-7s  %s\n",
						strings.Repeat(" ", 4), child.SubmodulePath, childStatus, commit)
				}
			}
		}
		return nil
	},
}

func init() {
	statusCmd.Flags().BoolVar(&statusSubmodules, "submodules", false, "Show nested submodule worktree status")
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Print pool status as JSON")
	rootCmd.AddCommand(statusCmd)
}

func writeStatusJSON(worktrees []pool.WorktreeStatus) error {
	output := make([]statusJSONWorktree, 0, len(worktrees))
	for _, wt := range worktrees {
		item := statusJSONWorktree{
			Name:        wt.Name,
			Path:        wt.Path,
			Status:      wt.Status,
			LeaseID:     wt.LeaseID,
			LeaseHolder: wt.LeaseHolder,
			Processes:   make([]statusJSONProcess, 0, len(wt.Processes)),
		}
		if !wt.LeasedAt.IsZero() {
			leasedAt := wt.LeasedAt
			item.LeasedAt = &leasedAt
		}
		for _, process := range wt.Processes {
			item.Processes = append(item.Processes, statusJSONProcess{
				PID:  process.PID,
				Name: process.Name,
			})
		}
		output = append(output, item)
	}
	return json.NewEncoder(os.Stdout).Encode(output)
}
