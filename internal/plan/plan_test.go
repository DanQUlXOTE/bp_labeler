package plan

import (
	"strings"
	"testing"

	"bplabeler/internal/bpclient"
)

var agents = []bpclient.Agent{
	{ID: "a1", HostName: "web-01", Labels: map[string]string{"env": "dev"}},
	{ID: "a2", HostName: "web-02", Labels: map[string]string{"env": "prod", "team": "x"}},
	{ID: "a3", HostName: "WEB-02", Labels: nil},
}

func TestReadCSVHeaderAndMerge(t *testing.T) {
	rows, _, err := ReadCSV(strings.NewReader("hostname,label\nweb-01,env=prod\nweb-01,\"team=db,tier=1\"\n"))
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	p := ForCSV(rows, agents)
	if len(p.Agents) != 1 || len(p.Agents[0].Changes) != 3 {
		t.Fatalf("plan=%+v", p.Agents)
	}
	ops := map[string]Op{}
	for _, c := range p.Agents[0].Changes {
		ops[c.Key] = c.Op
	}
	if ops["env"] != OpChange || ops["team"] != OpAdd || ops["tier"] != OpAdd {
		t.Fatalf("ops=%v", ops)
	}
}

func TestForCSVMatching(t *testing.T) {
	rows, _, _ := ReadCSV(strings.NewReader("web-02,env=prod\na1,env=dev\nghost,env=x\n"))
	p := ForCSV(rows, agents)
	if len(p.Agents) != 3 || len(p.Unmatched) != 1 || p.Unmatched[0] != "ghost" {
		t.Fatalf("agents=%d unmatched=%v", len(p.Agents), p.Unmatched)
	}
	if p.Pending() != 1 { // a2 unchanged, a1 unchanged, a3 add
		t.Fatalf("pending=%d", p.Pending())
	}
	if len(p.Warnings) != 1 {
		t.Fatalf("expected duplicate hostname warning, got %v", p.Warnings)
	}
}

func TestRemoveAndGroups(t *testing.T) {
	p := ForAgents(agents, map[string]string{"team": ""}, nil)
	if p.Pending() != 1 {
		t.Fatalf("pending=%d", p.Pending())
	}
	g := p.groups()
	if len(g) != 1 || !g[0].overwrite || g[0].ids[0] != "a2" {
		t.Fatalf("groups=%+v", g)
	}
	add := ForAgents(agents[2:], map[string]string{"new": "1"}, nil).groups()
	if len(add) != 1 || add[0].overwrite {
		t.Fatalf("add groups=%+v", add)
	}
}

func TestBadCSV(t *testing.T) {
	if _, _, err := ReadCSV(strings.NewReader("host,label\nweb-01,=x\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestFleetLabelBlocked(t *testing.T) {
	if _, err := ParseLabels("env=a,Fleet=NewWindowsFleet"); err == nil {
		t.Fatal("fleet label must be rejected")
	}
	if _, _, err := ReadCSV(strings.NewReader("web-01,fleet=x\n")); err == nil {
		t.Fatal("fleet label in csv must be rejected")
	}
}

func TestForAgentsFleet(t *testing.T) {
	f := &bpclient.Fleet{Name: "fleet-abc123", DisplayName: "NewWindowsFleet"}
	p := ForAgents(agents[:1], map[string]string{"env": "dev"}, f)
	var got Change
	for _, c := range p.Agents[0].Changes {
		if c.Key == FleetKey {
			got = c
		}
	}
	if got.New != "fleet-abc123" || got.Op != OpAdd {
		t.Fatalf("fleet change=%+v", got)
	}
}

func TestRemoveSyntax(t *testing.T) {
	m, err := ParseLabels("-team,tier=,env=prod")
	if err != nil || m["team"] != "" || m["tier"] != "" || m["env"] != "prod" || len(m) != 3 {
		t.Fatalf("m=%v err=%v", m, err)
	}
	if _, err := ParseLabels("-fleet"); err != nil {
		t.Fatalf("removing the fleet label should be allowed: %v", err)
	}
}

func TestVersionFilterAndUpgrade(t *testing.T) {
	as := []bpclient.Agent{{ID: "1", Version: "v1.60.0"}, {ID: "2", Version: "1.600.0"}, {ID: "3", Version: "1.61.2"}}
	if got := FilterVersion(as, "1.60"); len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("got %v", got)
	}
	if got := FilterVersion(as, "v1.61.2"); len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("got %v", got)
	}
	p := ForUpgrade(as, "1.61.2")
	if p.Pending() != 2 {
		t.Fatalf("pending=%d", p.Pending())
	}
	vs := []bpclient.AgentVersion{{Version: "v1.9.0"}, {Version: "v1.60.0"}, {Version: "v1.10.1"}}
	SortVersions(vs)
	if vs[0].Version != "v1.60.0" || vs[2].Version != "v1.9.0" {
		t.Fatalf("order=%v", vs)
	}
}

func TestCSVHeaderDrivesKeyAndLookup(t *testing.T) {
	rows, warns, err := ReadCSV(strings.NewReader("hostname,environment\nweb-01,Production\nweb-02,\nweb-03,team=db\n"))
	if err != nil || len(rows) != 2 || len(warns) != 1 {
		t.Fatalf("rows=%v warns=%v err=%v", rows, warns, err)
	}
	if rows[0].Lookup != LookupHostname || rows[0].Labels["environment"] != "Production" {
		t.Fatalf("row0=%+v", rows[0])
	}
	if rows[1].Labels["team"] != "db" || len(rows[1].Labels) != 1 {
		t.Fatalf("explicit key=value must still work: %+v", rows[1])
	}
	if _, _, err := ReadCSV(strings.NewReader("hostname,fleet\nweb-01,x\n")); err == nil {
		t.Fatal("a fleet column must be rejected")
	}
}

func TestCSVLookupModes(t *testing.T) {
	as := []bpclient.Agent{
		{ID: "ito1", HostName: "other"},               // an ID that looks like a hostname elsewhere
		{ID: "z9", HostName: "ito1.corp.example.com"}, // FQDN in Bindplane, short name in CMDB
		{ID: "z8", HostName: "ito2.corp.example.com"},
	}
	byHost := ForCSV([]Row{{Identifier: "ito1", Lookup: LookupHostname, Labels: map[string]string{"e": "p"}}}, as)
	if len(byHost.Agents) != 1 || byHost.Agents[0].Agent.ID != "z9" {
		t.Fatalf("hostname lookup must skip IDs and match the short name: %+v", byHost.Agents)
	}
	if len(byHost.Warnings) != 1 || !strings.Contains(byHost.Warnings[0], "short name") {
		t.Fatalf("short-name match should be reported: %v", byHost.Warnings)
	}
	byID := ForCSV([]Row{{Identifier: "ito1", Lookup: LookupID, Labels: map[string]string{"e": "p"}}}, as)
	if len(byID.Agents) != 1 || byID.Agents[0].Agent.ID != "ito1" {
		t.Fatalf("id lookup: %+v", byID.Agents)
	}
	if p := ForCSV([]Row{{Identifier: "ito2.corp", Lookup: LookupHostname, Labels: map[string]string{"e": "p"}}}, as); len(p.Unmatched) != 1 {
		t.Fatal("a dotted identifier must not use the short-name fallback")
	}
}
