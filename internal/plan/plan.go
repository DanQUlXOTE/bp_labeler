// Package plan turns desired labels into a per-agent change set (the dry
// run) and executes it against Bindplane.
package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"bplabeler/internal/bpclient"
)

type Op string

const (
	OpAdd    Op = "add"
	OpChange Op = "change"
	OpRemove Op = "remove"
	OpNoop   Op = "unchanged"
)

type Change struct {
	Key string
	Old string
	New string
	Op  Op
}

type AgentPlan struct {
	Agent   bpclient.Agent
	Changes []Change
}

// Pending reports whether any change would actually modify the agent.
func (a AgentPlan) Pending() bool {
	for _, c := range a.Changes {
		if c.Op != OpNoop {
			return true
		}
	}
	return false
}

type Plan struct {
	Agents    []AgentPlan
	Unmatched []string // CSV identifiers that matched no agent
	Warnings  []string
	// FleetNames maps a fleet resource name to its display name, for output.
	FleetNames map[string]string
}

// FleetKey is the label that assigns an agent to a fleet. Its value is the
// fleet's resource name, which is not stable or human readable, so it can
// only be set by choosing a fleet, never typed as a label.
const FleetKey = "fleet"

// Pending returns the number of agents with at least one real change.
func (p *Plan) Pending() int {
	n := 0
	for _, a := range p.Agents {
		if a.Pending() {
			n++
		}
	}
	return n
}

// ParseLabels parses "k=v,k2=v2". A key with an empty value ("k=") means
// delete that label.
func ParseLabels(s string) (map[string]string, error) {
	out := map[string]string{}
	if err := checkNoFleet(s); err != nil {
		return nil, err
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.HasPrefix(part, "-") && !strings.Contains(part, "=") {
			if k := strings.TrimSpace(part[1:]); k != "" {
				out[k] = "" // "-key" removes, same as "key="
				continue
			}
		}
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid label %q, expected key=value (or -key / key= to remove)", part)
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no labels given")
	}
	return out, nil
}

func checkNoFleet(s string) error {
	for _, part := range strings.Split(s, ",") {
		k, _, _ := strings.Cut(part, "=")
		if strings.EqualFold(strings.TrimSpace(k), FleetKey) {
			return fmt.Errorf("the %q label can't be set directly: it must be the fleet's resource name. Choose a fleet from the Fleet line in the TUI instead", FleetKey)
		}
	}
	return nil
}

// ForAgents builds a plan applying the same labels, and optionally a fleet
// assignment, to every agent.
func ForAgents(agents []bpclient.Agent, labels map[string]string, fleet *bpclient.Fleet) *Plan {
	p := &Plan{}
	if fleet != nil {
		withFleet := map[string]string{FleetKey: fleet.Name}
		for k, v := range labels {
			withFleet[k] = v
		}
		labels = withFleet
		p.FleetNames = map[string]string{fleet.Name: fleet.Title()}
	}
	for _, a := range agents {
		p.Agents = append(p.Agents, AgentPlan{Agent: a, Changes: diff(a.Labels, labels)})
	}
	return p
}

func diff(cur, want map[string]string) []Change {
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Change
	for _, k := range keys {
		v := want[k]
		old, exists := cur[k]
		c := Change{Key: k, Old: old, New: v}
		switch {
		case v == "" && exists:
			c.Op = OpRemove
		case v == "":
			c.Op = OpNoop
		case !exists:
			c.Op = OpAdd
		case old != v:
			c.Op = OpChange
		default:
			c.Op = OpNoop
		}
		out = append(out, c)
	}
	return out
}

// ForCSV resolves CSV rows (identifier, labels) against the given agents.
// An identifier matches an agent ID exactly, or a hostname/name
// case-insensitively; a hostname shared by several agents matches all of
// them. Multiple rows for one identifier are merged.
func ForCSV(rows []Row, agents []bpclient.Agent) *Plan {
	byID := map[string]int{}
	byHost := map[string][]int{}
	byShort := map[string][]int{} // first DNS label of the hostname, for CMDBs that export short names
	add := func(m map[string][]int, k string, i int) {
		if k == "" {
			return
		}
		for _, e := range m[k] {
			if e == i {
				return
			}
		}
		m[k] = append(m[k], i)
	}
	short := func(h string) string { return strings.SplitN(h, ".", 2)[0] }
	for i, a := range agents {
		byID[a.ID] = i
		hn, nm := strings.ToLower(a.HostName), strings.ToLower(a.Name)
		add(byHost, hn, i)
		add(byHost, nm, i)
		add(byShort, short(hn), i)
		add(byShort, short(nm), i)
	}

	p := &Plan{}
	desired := map[int]map[string]string{}
	var order []int
	unmatched := map[string]bool{}

	for _, r := range rows {
		var idx []int
		via := ""
		lower := strings.ToLower(r.Identifier)
		if i, ok := byID[r.Identifier]; ok && r.Lookup != LookupHostname {
			idx = []int{i}
		}
		if len(idx) == 0 && r.Lookup != LookupID {
			idx = byHost[lower]
			if len(idx) == 0 && !strings.Contains(lower, ".") {
				if idx = byShort[lower]; len(idx) > 0 {
					via = " by short name"
				}
			}
		}
		if len(idx) == 0 {
			if !unmatched[r.Identifier] {
				unmatched[r.Identifier] = true
				p.Unmatched = append(p.Unmatched, r.Identifier)
			}
			continue
		}
		if len(idx) > 1 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%q matches %d agents%s; labeling all of them", r.Identifier, len(idx), via))
		} else if via != "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%q matched %s%s", r.Identifier, agents[idx[0]].HostName, via))
		}
		for _, i := range idx {
			if desired[i] == nil {
				desired[i] = map[string]string{}
				order = append(order, i)
			}
			for k, v := range r.Labels {
				if prev, ok := desired[i][k]; ok && prev != v {
					p.Warnings = append(p.Warnings, fmt.Sprintf("%s: label %q set to both %q and %q; using %q", agents[i].ID, k, prev, v, v))
				}
				desired[i][k] = v
			}
		}
	}
	for _, i := range order {
		p.Agents = append(p.Agents, AgentPlan{Agent: agents[i], Changes: diff(agents[i].Labels, desired[i])})
	}
	return p
}

type group struct {
	labels    map[string]string
	ids       []string
	overwrite bool
}

// groups batches agents that need the identical change into one request.
func (p *Plan) groups() []group {
	idx := map[string]*group{}
	var order []string
	for _, ap := range p.Agents {
		labels := map[string]string{}
		overwrite := false
		for _, c := range ap.Changes {
			if c.Op == OpNoop {
				continue
			}
			labels[c.Key] = c.New
			if c.Op != OpAdd {
				overwrite = true
			}
		}
		if len(labels) == 0 {
			continue
		}
		keys := make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&sb, "%q=%q;", k, labels[k])
		}
		sig := sb.String()
		g, ok := idx[sig]
		if !ok {
			g = &group{labels: labels}
			idx[sig] = g
			order = append(order, sig)
		}
		g.ids = append(g.ids, ap.Agent.ID)
		g.overwrite = g.overwrite || overwrite
	}
	out := make([]group, 0, len(order))
	for _, s := range order {
		out = append(out, *idx[s])
	}
	return out
}

type Result struct {
	Labels  map[string]string
	Agents  int
	Updated int64
	Failed  []string
	Err     error
}

// Execute applies the plan. It continues past failed batches so one bad
// request does not strand the rest.
func (p *Plan) Execute(ctx context.Context, c *bpclient.Client) []Result {
	var out []Result
	for _, g := range p.groups() {
		res, err := c.LabelAgents(ctx, g.ids, g.labels, g.overwrite)
		out = append(out, Result{Labels: g.labels, Agents: len(g.ids), Updated: res.Count, Failed: res.FailedIDs, Err: err})
	}
	return out
}
