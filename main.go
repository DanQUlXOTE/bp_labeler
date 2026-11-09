package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"bplabeler/internal/bpclient"
	"bplabeler/internal/plan"
	"bplabeler/internal/profiles"
	"bplabeler/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		server, csvPath, query, selector, version, profileName string
		apiKeyEnv, user, pass                                  string
		apply, skipTLS                                         bool
	)
	flag.StringVar(&server, "server", os.Getenv("BINDPLANE_URL"), "Bindplane server URL (env: $BINDPLANE_URL). Passing --server explicitly overrides the default profile")
	flag.StringVar(&csvPath, "csv", "", "CSV: column 1 hostname or agent id, column 2 key=value labels or (with a header) bare values for the header's key. Runs headless instead of the TUI")
	flag.StringVar(&query, "query", "", "Initial collector query for the TUI, e.g. os:linux")
	flag.StringVar(&selector, "selector", "", "Initial label selector for the TUI, e.g. env=prod")
	flag.StringVar(&version, "version", "", "Initial current-version filter for the TUI, e.g. 1.60.0 or 1.60")
	flag.StringVar(&profileName, "profile", "", "Saved profile to connect with (default: the profile marked default; manage profiles in the TUI with p)")
	flag.BoolVar(&apply, "apply", false, "With --csv: actually apply label changes (default is a dry run). The TUI always starts as a dry run and asks for confirmation")
	flag.StringVar(&apiKeyEnv, "api-key-env", "BINDPLANE_API_KEY", "Env var holding the Bindplane API key")
	flag.StringVar(&user, "user", os.Getenv("BINDPLANE_USER"), "Basic-auth username (alternative to API key)")
	flag.StringVar(&pass, "pass", os.Getenv("BINDPLANE_PASSWORD"), "Basic-auth password")
	flag.BoolVar(&skipTLS, "skip-tls-verify", false, "Skip TLS certificate verification (lab only)")
	flag.Parse()

	flagCreds := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "server", "user", "pass", "api-key-env":
			flagCreds = true
		}
	})
	store, active, client, err := connect(profileName, flagCreds, server, os.Getenv(apiKeyEnv), user, pass, skipTLS)
	if err != nil {
		return err
	}

	if csvPath == "" {
		model := tui.New(client, query, selector, version)
		if store != nil {
			model = model.WithProfiles(store, active)
		}
		_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
		return err
	}
	if client == nil {
		return fmt.Errorf("no credentials: use --profile <name>, or set --server/$BINDPLANE_URL and an API key")
	}
	return runCSV(client, csvPath, apply)
}

func runCSV(client *bpclient.Client, path string, apply bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	rows, warns, err := plan.ReadCSV(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	ctx := context.Background()
	agents, err := client.ListAgents(ctx, "", "")
	if err != nil {
		return err
	}
	p := plan.ForCSV(rows, agents)
	p.Warnings = append(warns, p.Warnings...)

	if apply {
		fmt.Println("APPLY")
	} else {
		fmt.Println("DRY RUN (no changes made; pass --apply to apply)")
	}
	fmt.Println(strings.Repeat("-", 40))
	p.Render(os.Stdout)
	if !apply {
		return nil
	}

	failed := false
	for _, r := range p.Execute(ctx, client) {
		if r.Err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "FAILED %d collector(s) %v: %v %v\n", r.Agents, r.Labels, r.Err, r.Failed)
			continue
		}
		fmt.Printf("OK %d collector(s) updated with %v\n", r.Updated, r.Labels)
	}
	if failed {
		return fmt.Errorf("some label updates failed")
	}
	return nil
}

// connect picks the connection, in order: --profile; credentials passed as
// command-line flags; the saved default profile; credentials from the
// environment. With none of those, client is nil and the TUI opens the
// profile manager.
func connect(profileName string, flagCreds bool, server, apiKey, user, pass string, skipTLS bool) (*profiles.Store, string, *bpclient.Client, error) {
	var store *profiles.Store
	if path, err := profiles.DefaultPath(); err == nil {
		store = profiles.NewStore(path)
	} else if profileName != "" {
		return nil, "", nil, err
	}
	fromProfile := func(p profiles.Profile) (*bpclient.Client, error) {
		return bpclient.New(bpclient.Options{BaseURL: p.Server, APIKey: p.APIKey, SkipTLSVerify: p.SkipTLS})
	}
	fromCreds := func() (*profiles.Store, string, *bpclient.Client, error) {
		c, err := bpclient.New(bpclient.Options{BaseURL: server, APIKey: apiKey, User: user, Pass: pass, SkipTLSVerify: skipTLS})
		return store, "environment", c, err
	}
	haveCreds := server != "" && (apiKey != "" || user != "")

	if profileName != "" {
		p, err := store.Get(profileName)
		if err != nil {
			return nil, "", nil, err
		}
		c, err := fromProfile(p)
		return store, p.Name, c, err
	}
	if flagCreds && haveCreds {
		return fromCreds()
	}
	if store != nil {
		if name, err := store.Default(); err == nil && name != "" {
			if p, err := store.Get(name); err == nil {
				if c, err := fromProfile(p); err == nil {
					return store, p.Name, c, nil
				}
			}
		}
	}
	if haveCreds {
		return fromCreds()
	}
	return store, "", nil, nil
}
