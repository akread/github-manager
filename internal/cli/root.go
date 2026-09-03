// Package cli implements the ghw command tree.
package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github-manager/internal/config"
	"github-manager/internal/store"
)

var dbPath string

var rootCmd = &cobra.Command{
	Use:   "ghw",
	Short: "Watch GitHub pull requests and review requests",
	Long: `ghw watches GitHub activity through the gh CLI.

"pulls" watches single pull requests for new comments, reviews, and check
results. "reviews" watches repositories for pull requests that request your
review. Each group has subscribe, unsubscribe, list, and watch commands; watch
opens a terminal UI that refreshes on a timer.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", store.DefaultPath(), "path to the sqlite database (env GHW_DB)")
	rootCmd.AddCommand(pullsCmd, reviewsCmd, configCmd)
}

// Execute runs the CLI.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "ghw: %v\n", err)
		os.Exit(1)
	}
}

func openStore() (*store.Store, error) {
	return store.Open(dbPath)
}

// loadConfig reads the config and the refresh interval for the watch
// commands.
func loadConfig() (*config.Config, time.Duration, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, 0, err
	}
	d, err := cfg.Interval()
	if err != nil {
		return nil, 0, err
	}
	return cfg, d, nil
}

func noFileCompletion(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}
