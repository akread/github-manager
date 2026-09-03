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

var pullsCmd = &cobra.Command{
	Use:   "pulls",
	Short: "Watch single pull requests for activity",
	Long: `Watches single pull requests. Each subscription remembers a commit point;
the watch shows comments, reviews, and review requests after that point as
new. A commit in the watch moves the point to the time of the last refresh
for that pull request only.`,
}

// pullURLCompletion completes watched pull request urls from the database.
func pullURLCompletion(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	st, err := openStore()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	defer st.Close()
	pulls, err := st.ListPulls()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var out []cobra.Completion
	for _, p := range pulls {
		out = append(out, cobra.CompletionWithDesc(p.URL, fmt.Sprintf("%s#%d", p.Repo, p.Number)))
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

var pullsSubscribeCmd = &cobra.Command{
	Use:               "subscribe <url>...",
	Short:             "Watch one or more pull requests",
	Args:              cobra.MinimumNArgs(1),
	ValidArgsFunction: noFileCompletion,
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		for _, arg := range args {
			ref, err := github.ParsePullURL(arg)
			if err != nil {
				return err
			}
			p, err := st.SubscribePull(store.Pull{URL: ref.URL, Domain: ref.Domain, Repo: ref.Repo, Number: ref.Number})
			if err != nil {
				return err
			}
			fmt.Println("subscribed", p.URL)
		}
		return nil
	},
}

var pullsUnsubscribeCmd = &cobra.Command{
	Use:               "unsubscribe <url>...",
	Short:             "Stop watching one or more pull requests",
	Args:              cobra.MinimumNArgs(1),
	ValidArgsFunction: pullURLCompletion,
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		for _, arg := range args {
			ref, err := github.ParsePullURL(arg)
			if err != nil {
				return err
			}
			if err := st.UnsubscribePull(ref.URL); err != nil {
				return err
			}
			fmt.Println("unsubscribed", ref.URL)
		}
		return nil
	},
}

var pullsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the watched pull requests",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		pulls, err := st.ListPulls()
		if err != nil {
			return err
		}
		if len(pulls) == 0 {
			fmt.Println("no watched pull requests")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "REPO\tNUMBER\tSINCE\tURL")
		for _, p := range pulls {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", p.Repo, p.Number, p.Since.Local().Format("2006-01-02 15:04"), p.URL)
		}
		return w.Flush()
	},
}

var (
	pullsWatchExpanded bool
	pullsWatchComments bool
)

var pullsWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Open the terminal UI for the watched pull requests",
	Long: `Opens the terminal UI. It refreshes every refresh_interval (default 5m).

Keys: j/k move, c commit the selected pull request, C commit all, s subscribe
a url, u unsubscribe, o open in the browser, r refresh, a show all or only
updates, m show or hide comment text, ? expand or collapse the help, q quit.

The help is one row; items that do not fit are cut, and "? help" appears at
the right edge. Press ? to wrap every item onto more rows.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, interval, err := loadConfig()
		if err != nil {
			return err
		}
		st, err := openStore()
		if err != nil {
			return err
		}
		defer st.Close()
		return tui.RunPulls(tui.PullsOptions{
			Store:    st,
			Load:     tui.PullLoader(github.NewClient(), cfg.Excluded),
			Interval: interval,
			Expanded: pullsWatchExpanded,
			Comments: pullsWatchComments,
		})
	},
}

func init() {
	pullsWatchCmd.Flags().BoolVar(&pullsWatchExpanded, "expanded", false, "start with every pull request shown, not only those with updates")
	pullsWatchCmd.Flags().BoolVar(&pullsWatchComments, "comments", false, "start with the text of new comments shown")
	pullsCmd.AddCommand(pullsSubscribeCmd, pullsUnsubscribeCmd, pullsListCmd, pullsWatchCmd)
}
