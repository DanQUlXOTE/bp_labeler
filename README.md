# bp_labeler

A terminal UI and CSV tool for bulk operations on Bindplane collectors: set or remove labels, assign a fleet, and upgrade collectors to an available version.

Nothing changes until you confirm. The TUI always shows a dry run first and requires you to type `confirm`. Headless CSV runs are a dry run unless you pass `--apply`.

## Build

```bash
go build -o bplabeler .
```

## Connecting

Profiles hold a server URL and an API key, so you can switch between organizations. Run `./bplabeler` with no credentials and the TUI opens the profile manager. Press `p` on the search screen to open it later.

The connection is chosen in this order:

1. `--profile <name>`
2. Credentials passed as flags (`--server`, `--user`, `--pass`, `--api-key-env`)
3. The saved default profile
4. `$BINDPLANE_URL` with `$BINDPLANE_API_KEY` (or `$BINDPLANE_USER` / `$BINDPLANE_PASSWORD`)

Profiles are stored as JSON in your user config directory (`~/Library/Application Support/bplabeler/profiles.json` on macOS), or at `$BPLABELER_CONFIG`. The file is written with 0600 permissions and holds API keys in plain text. Keys are masked on screen.

## TUI

```bash
./bplabeler [--query os:linux] [--selector env=prod] [--version 1.60]
```

| Screen | Keys |
|---|---|
| Search (Query, Selector, Version) | up/down move between lines, enter edits a line, `s` searches, `p` profiles, esc quits |
| Collector list | space toggles, `a` all, `n` none, `[` `]` scroll the detail pane, enter labels the selection, `u` upgrades it, `/` new search |
| Labels | Set, Remove and Fleet lines, plus a labels pane. `v` previews. Down from Fleet enters the pane, where enter expands a label to show its values and collectors |
| Preview | up/down scroll, `a` applies (then type `confirm`), `b` goes back |
| Profiles | enter uses a profile, `s` sets or clears the default, `a` add, `e` edit, `d` delete, `t` tests a connection in the form |

Text lines use an input mode: press enter to edit, enter or esc to finish.

Label syntax is `key=value,key2=value2`. Remove a label with `-key` or `key=`, or use the Remove line.

### Fleets

Bindplane assigns a collector to a fleet through the `fleet` label, whose value is the fleet's resource name, not its display name. Typing `fleet=...` is blocked. Choose a fleet from the Fleet line, which looks up your fleets and lets you filter them. Removing the `fleet` label is allowed.

### Upgrades

Search with a Version filter (an exact version or a prefix such as `1.60`) and a label selector, select collectors, then press `u`. The picker lists the versions Bindplane offers, newest first. The preview shows each `from -> to` and skips collectors already at the target. Bindplane accepts the request and upgrades asynchronously, and it silently skips collectors that do not support upgrades.

## CSV mode

```bash
./bplabeler --csv hosts.csv            # dry run: print what would change
./bplabeler --csv hosts.csv --apply    # apply
```

Column 1 is a hostname or agent ID. Column 2 is a label.

```csv
hostname,environment
web01,Production
web02,Development
```

- With a header row, the first header chooses the lookup: `hostname` (also `host`, `name`, `server`, `fqdn`) matches hostnames, `id` or `agent_id` matches agent IDs exactly. Any other header tries ID, then hostname.
- The second header becomes the label key for bare values, so `web01,Production` sets `environment=Production`. A cell like `team=db` is used as written, and `-team` removes a label.
- Hostnames match case-insensitively. If there is no exact match and the CSV name has no dots, it matches the first part of a Bindplane hostname (`web01` matches `web01.corp.example.com`) and says so in the output.
- A hostname shared by several collectors labels all of them and prints a warning. Use agent IDs to target one.
- Blank values are skipped with a warning. A `fleet` column is rejected.

## Pulling hostnames from the Bindplane CLI

```bash
bindplane get agents --limit 10000 -o json | jq -r '["hostname","id"], (.[] | [.hostname, .id]) | @csv'
```

## Tests

```bash
go test ./...
```
