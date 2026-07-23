package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/treehouse/internal/config"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Print resolved Treehouse home, worktrees root, and user config paths",
	Long: `Print the effective TREEHOUSE_HOME, TREEHOUSE_WORKTREES, and user config
paths. When the environment variables are unset, prints the defaults derived
from $HOME so scripts and devcontainers can inspect what would be used.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := config.DataDir()
		if err != nil {
			return err
		}

		cfg, err := config.LoadGlobal()
		if err != nil {
			return err
		}
		worktrees, err := config.ResolvePoolRoot("", cfg.Root)
		if err != nil {
			return err
		}

		userConfig, err := config.UserConfigPath()
		if err != nil {
			return err
		}

		fmt.Fprintf(os.Stdout, "TREEHOUSE_HOME=%s\n", home)
		fmt.Fprintf(os.Stdout, "TREEHOUSE_WORKTREES=%s\n", worktrees)
		fmt.Fprintf(os.Stdout, "USER_CONFIG=%s\n", userConfig)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(envCmd)
}
