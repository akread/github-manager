package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github-manager/internal/github"
	"github-manager/internal/store"
	"github-manager/internal/tui"
)

var reviewsCmd = &cobra.Command{
	Use:   "reviews",
	Short: "Watch repositories for review requests",
	Long: `Watches repositories for open pull requests that request your review. The
watch marks a request as new until you commit it as seen.`,
}

// repoCompletion completes watched repositories from the database.
func repoCompletion(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	st, err := openStore()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	defer st.Close()
	repos, err := st.ListRepos()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var out []cobra.Completion
	for _, r := range repos {
		ref := github.RepoRef{Domain: r.Domain, Repo: r.Repo}
		if r.Domain == "github.com" {
			out = append(out, cobra.CompletionWithDesc(r.Repo, ref.URL()))
		} else {
			out = append(out, cobra.CompletionWithDesc(ref.URL(), r.Repo))
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

var reviewsSubscribeCmd = &cobra.Command{
	Use:               "subscribe <repo>...",
	Short:             "Watch one or more repositories (owner/name or url)",
	Args:              cobra.MinimumNArgs(1),
	ValidArgsFunction: noFileCompletion,
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		for _, arg := range args {
			ref, err := github.ParseRepo(arg)
			if err != nil {
				return err
			}
			r, err := st.SubscribeRepo(store.Repo{Domain: ref.Domain, Repo: ref.Repo})
			if err != nil {
				return err
			}
			fmt.Println("subscribed", r.Key())
		}
		return nil
	},
}

var reviewsUnsubscribeCmd = &cobra.Command{
	Use:               "unsubscribe <repo>...",
	Short:             "Stop watching one or more repositories",
	Args:              cobra.MinimumNArgs(1),
	ValidArgsFunction: repoCompletion,
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		for _, arg := range args {
			ref, err := github.ParseRepo(arg)
			if err != nil {
				return err
			}
			if err := st.UnsubscribeRepo(ref.Domain, ref.Repo); err != nil {
				return err
			}
			fmt.Println("unsubscribed", ref.Domain+"/"+ref.Repo)
		}
		return nil
	},
}

var reviewsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the watched repositories",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		repos, err := st.ListRepos()
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			fmt.Println("no watched repositories")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "REPO\tURL")
		for _, r := range repos {
			fmt.Fprintf(w, "%s\t%s\n", r.Repo, github.RepoRef{Domain: r.Domain, Repo: r.Repo}.URL())
		}
		return w.Flush()
	},
}

var reviewsWatchExpanded bool

var reviewsWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Open the terminal UI for review requests",
	Long: `Opens the terminal UI. It refreshes every refresh_interval (default 5m).

Keys: j/k move, c commit the selected request as seen, C commit all, s
subscribe the selected pull request under pulls, o open in the browser, r
refresh, a show all or only new, ? expand or collapse the help, q quit.

The help is one row; items that do not fit are cut, and "? help" appears at
the right edge. Press ? to wrap every item onto more rows.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, interval, err := loadConfig()
		if err != nil {
			return err
		}
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		return tui.RunReviews(tui.ReviewsOptions{
			Store:    st,
			Load:     tui.ReviewLoader(github.NewClient()),
			Interval: interval,
			Expanded: reviewsWatchExpanded,
		})
	},
}

func init() {
	reviewsWatchCmd.Flags().BoolVar(&reviewsWatchExpanded, "expanded", false, "start with every review request shown, not only the new ones")
	reviewsCmd.AddCommand(reviewsSubscribeCmd, reviewsUnsubscribeCmd, reviewsListCmd, reviewsWatchCmd)
}
