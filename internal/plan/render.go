package plan

import (
	"fmt"
	"io"
	"strings"
)

func (c Change) String() string {
	switch c.Op {
	case OpAdd:
		return fmt.Sprintf("+ %s=%s", c.Key, c.New)
	case OpChange:
		return fmt.Sprintf("~ %s: %s -> %s", c.Key, c.Old, c.New)
	case OpRemove:
		return fmt.Sprintf("- %s (was %s)", c.Key, c.Old)
	}
	return fmt.Sprintf("= %s=%s (already set)", c.Key, c.Old)
}

func label(a AgentPlan) string {
	if a.Agent.HostName != "" {
		return fmt.Sprintf("%s (%s)", a.Agent.HostName, a.Agent.ID)
	}
	return a.Agent.ID
}

// Render writes the human readable change report.
func (p *Plan) Render(w io.Writer) {
	for _, a := range p.Agents {
		fmt.Fprintln(w, label(a))
		for _, c := range a.Changes {
			fmt.Fprintf(w, "    %s", c)
			if n, ok := p.FleetNames[c.New]; ok && c.Key == FleetKey {
				fmt.Fprintf(w, "  [fleet: %s]", n)
			}
			fmt.Fprintln(w)
		}
	}
	if len(p.Unmatched) > 0 {
		fmt.Fprintf(w, "\nNo matching collector (%d):\n", len(p.Unmatched))
		for _, u := range p.Unmatched {
			fmt.Fprintf(w, "    %s\n", u)
		}
	}
	if len(p.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, s := range p.Warnings {
			fmt.Fprintf(w, "    %s\n", s)
		}
	}
	fmt.Fprintf(w, "\n%s\n", p.Summary())
}

func (p *Plan) Summary() string {
	var add, chg, rm, noop int
	for _, a := range p.Agents {
		for _, c := range a.Changes {
			switch c.Op {
			case OpAdd:
				add++
			case OpChange:
				chg++
			case OpRemove:
				rm++
			default:
				noop++
			}
		}
	}
	parts := []string{
		fmt.Sprintf("%d collector(s) would change", p.Pending()),
		fmt.Sprintf("%d added", add), fmt.Sprintf("%d changed", chg),
		fmt.Sprintf("%d removed", rm), fmt.Sprintf("%d unchanged", noop),
	}
	if n := len(p.Unmatched); n > 0 {
		parts = append(parts, fmt.Sprintf("%d identifier(s) unmatched", n))
	}
	return strings.Join(parts[:1], "") + ": " + strings.Join(parts[1:], ", ")
}
