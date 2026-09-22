# github manager

Monitor pull requests and such.

This repository holds two implementations side by side: the original JavaScript tool under `src/`, and its Go replacement under `cmd/` and `internal/`. The rest of this file describes the Go tool.

## ghw

`ghw` watches GitHub activity through the `gh` CLI. It needs `gh` on your `PATH`, logged in to each GitHub host you watch (`gh auth login --hostname <host>`).

- `pulls` watches single pull requests for new comments, review comments, approvals, requested changes, review requests, and required check results.
- `reviews` watches repositories for open pull requests that request your review.

Each group has `subscribe`, `unsubscribe`, `list`, and `watch` commands. `watch` opens a terminal UI that refreshes on a timer.

### install

```sh
go install ./cmd/ghw
```

Tab completion comes from cobra. For zsh, add this line to `~/.zshrc` after `compinit`:

```sh
source <(ghw completion zsh)
```

`ghw completion bash|fish|powershell` print the scripts for the other shells. The `unsubscribe` commands complete from your subscriptions, and `config` completes its keys.

### usage

```sh
ghw pulls subscribe https://github.com/owner/name/pull/123
ghw pulls unsubscribe https://github.com/owner/name/pull/123
ghw pulls list
ghw pulls watch [--expanded] [--no-comments] [--mine]

ghw reviews subscribe owner/name             # github.com
ghw reviews subscribe https://ghe.example.com/owner/name
ghw reviews unsubscribe owner/name
ghw reviews list
ghw reviews watch [--expanded]
```

### pulls watch

Each subscription has a commit point. The watch shows activity after that point as new: comments, review comments, approvals, and requested changes. A pull request also counts as an update when it is closed, or when a review request for you arrives after that point. After a commit the `Review requested` row stays in the dim style until you submit the review.

A pull request in a merge queue shows `[QUEUED]` in place of `[OPEN]`, with its position and the state of the queue entry. On a branch with a merge queue, gh ignores the merge method: the queue applies the method from the branch rule, and the pull request stays open until the queue merges it.

By default the watch shows only pull requests with updates. Comments from you, from non-user accounts, and from the excluded usernames of the domain are ignored. Your own pull requests show your username underlined. Press `A` to show only those.

| Key | Action |
| --- | --- |
| `j` / `k` | move |
| `c` | commit the selected pull request: its point moves to the last refresh. A closed pull request is unsubscribed instead. |
| `C` | commit every pull request |
| `s` | subscribe to a pull request url |
| `u` | unsubscribe the selected pull request |
| `o` | open the selected pull request in the browser |
| `M` | merge the selected pull request with `gh pr merge`. A prompt asks for the method: `m` merge, `s` squash, `r` rebase, or `esc` to cancel. A second prompt asks `are you sure`; press `y` to merge, or `n` to cancel. The repository settings decide whether the method is allowed and whether the branch is deleted. After the merge the watch reloads that pull request and reports the result: `merged`, or `added to the merge queue` when the base branch has a merge queue. |
| `r` | refresh now |
| `/` | filter the list by title, repo, number, author, or url |
| `a` | show every pull request, or only those with updates |
| `A` | show only your own pull requests, or those from every author |
| `m` | show or hide the text of new comments; the text shows by default, and the header reads `comments hidden` when it is off |
| `?` | expand or collapse the help |
| `q` | quit |

The filter is a case-insensitive substring match. The list filters as you type. Press `enter` to keep the filter and return to the list, or `esc` to clear it. The header shows the active filter, and `esc` in the list clears it.

The commit is per pull request. The JavaScript tool committed every pull request at once.

Both watch screens take mouse input: the wheel scrolls the list, and a left click moves the cursor to the row under the pointer.

The help is one row. The `j` / `k` and `C` shortcuts are hidden from it, and `u` in the pulls watch, so the right edge reads `? help · q quit`. When the other shortcuts do not fit, the row cuts them with an ellipsis. Press `?` to show every shortcut, wrapped onto as many rows as it needs, and again to collapse it. The right-edge keys stay on the bottom row in both states.

### reviews watch

The watch lists the open pull requests that request your review, grouped by repository. A request is new until you commit it as seen. A request that you also watch under `pulls` shows `(watching)`.

| Key | Action |
| --- | --- |
| `j` / `k` | move |
| `c` | commit the selected request as seen |
| `C` | commit every request as seen, and forget seen requests that are no longer open |
| `s` | subscribe the selected pull request under `pulls` |
| `o` | open the selected pull request, or the repository, in the browser |
| `r` | refresh now |
| `a` | show every request, or only the new ones |
| `?` | expand or collapse the help |
| `q` | quit |

### configuration

The config file is TOML at `~/.config/ghw/config.toml`. `$XDG_CONFIG_HOME` or `$GHW_CONFIG` override the location.

```toml
refresh_interval = "5m"   # watch refresh interval; default 5m

[domains."github.com"]
excluded_usernames = ["svc-bot-account"]   # comment authors to ignore
```

`ghw config get/set/add/delete <key> [value...]` edit the file with dotted keys. A key segment with a dot goes in double quotes. `set` replaces the whole value of an array key and accepts many values; `add` appends one value to an array key. `ghw config list` prints every key, `config path` prints the location, and `config edit` opens the file in `$VISUAL`, `$EDITOR`, or `vi`.

```sh
ghw config set refresh_interval 2m
ghw config add 'domains."github.com".excluded_usernames' svc-bot-account
ghw config delete 'domains."github.com".excluded_usernames' svc-bot-account
```

### data

Subscriptions live in SQLite at `~/.local/share/ghw/ghw.db`. `$XDG_DATA_HOME`, `$GHW_DB`, or the `--db` flag override the location. The JavaScript tool stored its data in `~/.github-manager`; the Go tool does not read it.

### development

```sh
go build ./...
go test ./...
```

### layout

```
cmd/ghw/    # main
internal/cli/      # cobra commands: pulls, reviews, config
internal/config/   # toml config: typed load, and get/set/add/delete on dotted keys
internal/store/    # sqlite: watched pulls, watched repos, seen review requests
internal/github/   # gh cli wrapper, url parsing, status derivation
internal/tui/      # bubbletea models for the two watch commands
```
