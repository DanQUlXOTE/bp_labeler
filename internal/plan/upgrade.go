package plan

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"bplabeler/internal/bpclient"
)

// NormVersion strips whitespace and a leading "v" so "v1.2.3" == "1.2.3".
func NormVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// FilterVersion keeps agents whose version equals want, or starts with it at
// a dot boundary ("1.60" matches 1.60.0 but not 1.600.0). Empty keeps all.
func FilterVersion(agents []bpclient.Agent, want string) []bpclient.Agent {
	want = NormVersion(want)
	if want == "" {
		return agents
	}
	var out []bpclient.Agent
	for _, a := range agents {
		have := NormVersion(a.Version)
		if have == want || strings.HasPrefix(have, want+".") {
			out = append(out, a)
		}
	}
	return out
}

// SortVersions orders versions newest first, comparing dotted numbers.
func SortVersions(v []bpclient.AgentVersion) {
	sort.SliceStable(v, func(i, j int) bool { return versionLess(v[j].Version, v[i].Version) })
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(NormVersion(a), "."), strings.Split(NormVersion(b), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		if ex == nil && ey == nil {
			if nx != ny {
				return nx < ny
			}
			continue
		}
		if x != y {
			return x < y
		}
	}
	return false
}

type UpgradeItem struct {
	Agent bpclient.Agent
	Skip  string // non-empty: reason this collector won't be upgraded
}

type UpgradePlan struct {
	Target string
	Items  []UpgradeItem
}

func ForUpgrade(agents []bpclient.Agent, target string) *UpgradePlan {
	p := &UpgradePlan{Target: target}
	for _, a := range agents {
		it := UpgradeItem{Agent: a}
		if NormVersion(a.Version) == NormVersion(target) {
			it.Skip = "already at target"
		}
		p.Items = append(p.Items, it)
	}
	return p
}

func (p *UpgradePlan) Pending() int {
	n := 0
	for _, it := range p.Items {
		if it.Skip == "" {
			n++
		}
	}
	return n
}

func (p *UpgradePlan) Render(w io.Writer) {
	offline := 0
	for _, it := range p.Items {
		a := it.Agent
		name := a.HostName
		if name == "" {
			name = a.Name
		}
		if it.Skip != "" {
			fmt.Fprintf(w, "= %s (%s)  %s  [%s]\n", name, a.ID, a.Version, it.Skip)
			continue
		}
		note := ""
		if !a.Connected {
			note = "  [offline]"
			offline++
		}
		fmt.Fprintf(w, "~ %s (%s)  %s -> %s%s\n", name, a.ID, a.Version, p.Target, note)
	}
	fmt.Fprintf(w, "\n%d collector(s) would be upgraded to %s, %d skipped", p.Pending(), p.Target, len(p.Items)-p.Pending())
	if offline > 0 {
		fmt.Fprintf(w, ", %d offline", offline)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Bindplane silently skips collectors that don't support upgrades.")
}

type UpgradeResult struct {
	Agents int
	Err    error
}

const upgradeBatch = 500

func (p *UpgradePlan) Execute(ctx context.Context, c *bpclient.Client) []UpgradeResult {
	var ids []string
	for _, it := range p.Items {
		if it.Skip == "" {
			ids = append(ids, it.Agent.ID)
		}
	}
	var out []UpgradeResult
	for len(ids) > 0 {
		n := min(upgradeBatch, len(ids))
		out = append(out, UpgradeResult{Agents: n, Err: c.UpgradeAgents(ctx, ids[:n], p.Target)})
		ids = ids[n:]
	}
	return out
}
