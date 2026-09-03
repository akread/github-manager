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
ghw pulls watch [--expanded] [--comments]

ghw reviews subscribe owner/name             # github.com
ghw reviews subscribe https://ghe.example.com/owner/name
ghw reviews unsubscribe owner/name
ghw reviews list
ghw reviews watch [--expanded]
```

### pulls watch

Each subscription has a commit point. The watch shows activity after that point as new: comments, review comments, approvals, and requested changes. A pull request also counts as an update when it is closed, or when it requests your review.

By default the watch shows only pull requests with updates. Comments from you, from non-user accounts, and from the excluded usernames of the domain are ignored.

| Key | Action |
| --- | --- |
| `j` / `k` | move |
| `c` | commit the selected pull request: its point moves to the last refresh. A closed pull request is unsubscribed instead. |
| `C` | commit every pull request |
| `s` | subscribe to a pull request url |
| `u` | unsubscribe the selected pull request |
| `o` | open the selected pull request in the browser |
| `r` | refresh now |
| `a` | show every pull request, or only those with updates |
| `m` | show or hide the text of new comments |
| `?` | expand or collapse the help |
| `q` | quit |

The commit is per pull request. The JavaScript tool committed every pull request at once.

Both watch screens take mouse input: the wheel scrolls the list, and a left click moves the cursor to the row under the pointer.

The help is one row with `q quit` at the right edge. When the shortcuts do not fit, the row cuts them with an ellipsis and the right edge reads `? help · q quit`. Press `?` to wrap every shortcut onto more rows, and again to collapse it. The right-edge keys stay on the bottom row in both states.

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
