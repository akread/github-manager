package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github-manager/internal/config"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Read and edit the ghw TOML configuration",
	Long: `Reads and edits the config file with dotted keys. A key segment with a dot
goes in double quotes:

  ghw config set refresh_interval 2m
  ghw config add 'domains."github.com".excluded_usernames' some-bot`,
}

// excludedKey is the dotted key of a domain's excluded usernames.
func excludedKey(domain string) string {
	return config.JoinKey([]string{"domains", domain, "excluded_usernames"})
}

// completeSchemaKeys offers every possible config key: the global keys, and
// the excluded_usernames key for each domain known from the config or the
// database. With arraysOnly, it offers only the array keys.
func completeSchemaKeys(arraysOnly bool) func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		cfg, err := config.Load()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		domains := map[string]bool{}
		for d := range cfg.Domains {
			domains[d] = true
		}
		if st, err := openStore(); err == nil {
			if ds, err := st.Domains(); err == nil {
				for _, d := range ds {
					domains[d] = true
				}
			}
			st.Close()
		}
		var out []cobra.Completion
		if !arraysOnly {
			out = append(out, cobra.CompletionWithDesc("refresh_interval", "watch refresh interval, e.g. 5m (single value)"))
		}
		for d := range domains {
			out = append(out, cobra.CompletionWithDesc(excludedKey(d), "comment authors to ignore on "+d+" (array)"))
		}
		sort.Strings(out)
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeExistingKeys offers the keys present in the config file.
func completeExistingKeys(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	f, err := config.LoadFile()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	return f.Keys(), cobra.ShellCompDirectiveNoFileComp
}

// completeDeleteArgs completes the key, then the elements of that array key
// for the optional value argument.
func completeDeleteArgs(cmd *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return completeExistingKeys(cmd, args, toComplete)
	}
	if len(args) != 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	f, err := config.LoadFile()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	v, err := f.Get(args[0])
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []cobra.Completion
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func printValue(v any) {
	switch val := v.(type) {
	case []any:
		for _, item := range val {
			fmt.Fprintln(os.Stdout, item)
		}
	case map[string]any:
		out, err := toml.Marshal(val)
		if err == nil {
			fmt.Fprint(os.Stdout, string(out))
		}
	default:
		fmt.Fprintln(os.Stdout, val)
	}
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the config file path",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := config.Path()
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, path)
		return nil
	},
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open the config file in your editor ($VISUAL, $EDITOR, or vi)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := config.Path()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			editor = "vi"
		}
		// the editor value can carry flags, e.g. "code --wait"
		argv := append(strings.Fields(editor), path)
		c := exec.Command(argv[0], argv[1:]...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = c.Run()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		return err
	},
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "Print every config key and its values",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := config.LoadFile()
		if err != nil {
			return err
		}
		keys := f.Keys()
		if len(keys) == 0 {
			fmt.Fprintf(os.Stderr, "ghw: no config yet (%s)\n", f.Path)
			return nil
		}
		for _, key := range keys {
			v, err := f.Get(key)
			if err != nil {
				return err
			}
			if arr, ok := v.([]any); ok {
				for _, item := range arr {
					fmt.Fprintf(os.Stdout, "%s = %v\n", key, item)
				}
				continue
			}
			fmt.Fprintf(os.Stdout, "%s = %v\n", key, v)
		}
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:               "get <key>",
	Short:             "Print a config value",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeExistingKeys,
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := config.LoadFile()
		if err != nil {
			return err
		}
		v, err := f.Get(args[0])
		if err != nil {
			return err
		}
		printValue(v)
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:               "set <key> <value>...",
	Short:             "Set a config value (replaces the whole value of an array key)",
	Args:              cobra.MinimumNArgs(2),
	ValidArgsFunction: completeSchemaKeys(false),
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := config.LoadFile()
		if err != nil {
			return err
		}
		if err := f.Set(args[0], args[1:]...); err != nil {
			return err
		}
		return f.Save()
	},
}

var configAddCmd = &cobra.Command{
	Use:               "add <key> <value>",
	Short:             "Append a value to an array config key",
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeSchemaKeys(true),
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := config.LoadFile()
		if err != nil {
			return err
		}
		if err := f.Add(args[0], args[1]); err != nil {
			return err
		}
		return f.Save()
	},
}

var configDeleteCmd = &cobra.Command{
	Use:               "delete <key> [value]",
	Short:             "Delete a config key, or one value from an array key",
	Args:              cobra.RangeArgs(1, 2),
	ValidArgsFunction: completeDeleteArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := config.LoadFile()
		if err != nil {
			return err
		}
		var value *string
		if len(args) == 2 {
			value = &args[1]
		}
		if err := f.Delete(args[0], value); err != nil {
			return err
		}
		return f.Save()
	},
}

func init() {
	configCmd.AddCommand(configPathCmd, configEditCmd, configListCmd, configGetCmd, configSetCmd, configAddCmd, configDeleteCmd)
}
