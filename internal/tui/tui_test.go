package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"bplabeler/internal/bpclient"
	"bplabeler/internal/profiles"

	tea "github.com/charmbracelet/bubbletea"
)

func send(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		nm, cmd := m.Update(msg)
		m = nm.(Model)
		if cmd != nil {
			// run commands that produce data messages (skip blink timers)
			if out := cmd(); out != nil {
				switch out.(type) {
				case fleetsMsg, agentsMsg, versionsMsg:
					nm, _ = m.Update(out)
					m = nm.(Model)
				}
			}
		}
	}
	return m
}

func key(s string) tea.Msg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestFleetPickerUsesResourceName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/fleets") {
			if r.URL.Query().Get("offset") != "0" {
				w.Write([]byte(`{"fleets":[]}`))
				return
			}
			w.Write([]byte(`{"fleets":[
			 {"metadata":{"name":"fleet-aaa","displayName":"Linux Fleet"},"spec":{"configuration":"c1"}},
			 {"metadata":{"name":"fleet-bbb","displayName":"NewWindowsFleet"},"spec":{"configuration":"c2"}}]}`))
		}
	}))
	defer srv.Close()
	c, _ := bpclient.New(bpclient.Options{BaseURL: srv.URL, APIKey: "x"})

	m := New(c, "", "", "")
	m.agents = []bpclient.Agent{{ID: "a1", HostName: "empire", Labels: map[string]string{}}}
	m.selected = map[string]bool{"a1": true}
	m.stage = stLabels

	// typing fleet= as a label must be rejected
	m = send(m, key("enter"), key("fleet=x"), key("enter"), key("v")) // edit, type, leave edit, verify
	if m.stage != stLabels || !strings.Contains(m.msg, "fleet") {
		t.Fatalf("fleet label not blocked: stage=%v msg=%q", m.stage, m.msg)
	}

	// pick the fleet by filtering on its display name
	m.labels.SetValue("")
	m = send(m, key("tab"), key("tab"), key("enter"))
	if m.stage != stFleet || len(m.fleetView) != 2 {
		t.Fatalf("picker: stage=%v view=%d", m.stage, len(m.fleetView))
	}
	m = send(m, key("windows"))
	if len(m.fleetView) != 1 {
		t.Fatalf("filter left %d fleets", len(m.fleetView))
	}
	m = send(m, key("enter"))
	if m.fleet == nil || m.fleet.Name != "fleet-bbb" || m.stage != stLabels {
		t.Fatalf("selection: %+v stage=%v", m.fleet, m.stage)
	}
	m = send(m, key("v"))
	if m.stage != stPreview {
		t.Fatalf("stage=%v msg=%q", m.stage, m.msg)
	}
	v := m.View()
	if !strings.Contains(v, "fleet=fleet-bbb") || !strings.Contains(v, "NewWindowsFleet") {
		t.Fatalf("preview missing fleet resource name:\n%s", v)
	}
}

func TestRemoveLine(t *testing.T) {
	m := New(nil, "", "", "")
	m.agents = []bpclient.Agent{{ID: "a1", HostName: "empire", Labels: map[string]string{"team": "x", "env": "dev"}}}
	m.selected = map[string]bool{"a1": true}
	m.stage = stLabels
	m = send(m, key("tab"), key("enter"), key("team"), key("enter"), key("v"))
	if m.stage != stPreview {
		t.Fatalf("stage=%v msg=%q", m.stage, m.msg)
	}
	if v := m.View(); !strings.Contains(v, "- team (was x)") || strings.Contains(v, "env") {
		t.Fatalf("preview:\n%s", v)
	}
}

func TestDetailPane(t *testing.T) {
	m := New(nil, "", "", "")
	lab := map[string]string{}
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		lab["attributes/"+k] = "value-" + k
	}
	m.agents = []bpclient.Agent{{ID: "id-1", HostName: "empire", Connected: true, Version: "1.2", Labels: lab}}
	m.stage = stList
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 28})
	v := m.View()
	for _, want := range []string{"empire  id=id-1", "attributes/a = value-a", "labels 1-"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in:\n%s", want, v)
		}
	}
	m = send(m, key("]"), key("]"), key("]"), key("]"), key("]"), key("]"), key("]"), key("]"), key("]"))
	if v := m.View(); !strings.Contains(v, "of 8") || !strings.Contains(v, "attributes/h") {
		t.Fatalf("scroll:\n%s", v)
	}
	for _, l := range strings.Split(v, "\n") {
		if len([]rune(l)) > 120 { // ansi escapes add bytes, plain width stays bounded
			t.Fatalf("line too long: %q", l)
		}
	}
}

func TestUpgradeFlow(t *testing.T) {
	var patched string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/agent-versions":
			w.Write([]byte(`{"agentVersions":[
			 {"spec":{"type":"bindplane-agent","version":"v1.9.0"}},
			 {"spec":{"type":"bindplane-agent","version":"v1.60.0"}},
			 {"spec":{"type":"bindplane-agent","version":"v1.70.0","draft":true}},
			 {"spec":{"type":"other","version":"v9.9.9"}}]}`))
		case r.URL.Path == "/v1/agents/version" && r.Method == http.MethodPatch:
			b := make([]byte, 512)
			n, _ := r.Body.Read(b)
			patched = string(b[:n])
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c, _ := bpclient.New(bpclient.Options{BaseURL: srv.URL, APIKey: "x"})
	m := New(c, "", "", "")
	m.agents = []bpclient.Agent{
		{ID: "a1", HostName: "h1", Type: "bindplane-agent", Version: "1.9.0"},
		{ID: "a2", HostName: "h2", Type: "bindplane-agent", Version: "v1.60.0"},
	}
	m.selected = map[string]bool{"a1": true, "a2": true}
	m.stage = stList

	m = send(m, key("u"))
	if m.stage != stVersion || len(m.verView) != 2 || m.verView[0].Version != "v1.60.0" {
		t.Fatalf("picker: stage=%v view=%v", m.stage, m.verView)
	}
	m = send(m, key("enter"))
	v := m.View()
	if m.stage != stPreview || m.up.Pending() != 1 || !strings.Contains(v, "1.9.0 -> v1.60.0") || !strings.Contains(v, "already at target") {
		t.Fatalf("preview:\n%s", v)
	}
	m = send(m, key("a"), key("confirm"), key("enter"))
	m = send(m, upgradedMsg{m.up.Execute(context.Background(), c)})
	if !strings.Contains(patched, `"ids":["a1"]`) || !strings.Contains(patched, `"version":"v1.60.0"`) {
		t.Fatalf("patch body: %s", patched)
	}
	if m.stage != stDone || !strings.Contains(m.View(), "requested for 1") {
		t.Fatalf("done:\n%s", m.View())
	}
}

func TestProfileManager(t *testing.T) {
	store := profiles.NewStore(filepath.Join(t.TempDir(), "p.json"))
	m := New(nil, "", "", "").WithProfiles(store, "")
	if m.stage != stProfiles {
		t.Fatalf("should start on profiles without a client, got %v", m.stage)
	}
	typeIn := func(m Model, s string) Model { // enter input mode, type, leave
		return send(m, key("enter"), key(s), key("enter"))
	}
	// add
	m = send(m, key("a"))
	m = typeIn(m, "acme")
	m = send(m, key("down"))
	m = typeIn(m, "https://acme.example.com")
	m = send(m, key("down"))
	m = typeIn(m, "secret-key-1234")
	m = send(m, key("s"))
	if m.stage != stProfiles || len(m.profs) != 1 || m.profs[0].APIKey != "secret-key-1234" {
		t.Fatalf("add: stage=%v profs=%+v msg=%q", m.stage, m.profs, m.msg)
	}
	if v := m.View(); strings.Contains(v, "secret-key-1234") || !strings.Contains(v, "****1234") {
		t.Fatalf("key must be masked:\n%s", v)
	}
	// edit: rename, key left blank so it is kept
	m = send(m, key("e"))
	m.pName.SetValue("acme-prod")
	m = send(m, key("s"))
	if m.profs[0].Name != "acme-prod" || m.profs[0].APIKey != "secret-key-1234" {
		t.Fatalf("edit: %+v msg=%q", m.profs, m.msg)
	}
	// use
	m = send(m, key("enter"))
	if m.stage != stSearch || m.client == nil || m.active != "acme-prod" {
		t.Fatalf("use: stage=%v active=%q", m.stage, m.active)
	}
	// back into manager with p, delete with confirmation
	m = send(m, key("p"), key("d"), key("n"))
	if len(m.profs) != 1 {
		t.Fatal("delete must need y")
	}
	m = send(m, key("d"), key("y"))
	if len(m.profs) != 0 || m.client != nil {
		t.Fatalf("delete: profs=%v client=%v", m.profs, m.client)
	}
}

func TestLabelsPane(t *testing.T) {
	m := New(nil, "", "", "")
	m.agents = []bpclient.Agent{
		{ID: "a1", HostName: "h1", Labels: map[string]string{"env": "prod", "attributes/os.arch": "amd64"}},
		{ID: "a2", HostName: "h2", Labels: map[string]string{"env": "dev", "attributes/os.arch": "amd64", "team": "x"}},
	}
	m.selected = map[string]bool{"a1": true, "a2": true}
	m.stage = stLabels
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	v := m.View()
	for _, want := range []string{"Current labels on 2 selected collector(s): 3 key(s)", "attributes/os.arch = amd64", "env  (2 different values)", "team = x  [on 1 of 2]"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in:\n%s", want, v)
		}
	}
	if strings.Contains(v, "> env=prod") || strings.Contains(v, ">  ") && strings.Count(v, "> ") > 1 {
		t.Fatalf("stray input prompt:\n%s", v)
	}
	t.Log("\n" + v)
}

func TestLabelsPaneExpand(t *testing.T) {
	m := New(nil, "", "", "")
	m.agents = []bpclient.Agent{
		{ID: "a1", HostName: "h1", Labels: map[string]string{"env": "prod"}},
		{ID: "a2", HostName: "h2", Labels: map[string]string{"env": "dev"}},
		{ID: "a3", HostName: "h3", Labels: map[string]string{"env": "dev"}},
	}
	m.selected = map[string]bool{"a1": true, "a2": true, "a3": true}
	m.stage = stLabels
	m = send(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if strings.Contains(m.View(), "h2, h3") {
		t.Fatal("collapsed by default")
	}
	m = send(m, key("down"), key("down"), key("down")) // Set -> Remove -> Fleet -> pane
	if m.labelFocus != 3 {
		t.Fatalf("focus=%d", m.labelFocus)
	}
	m = send(m, key("enter"))
	v := m.View()
	if !strings.Contains(v, "[-] env") || !strings.Contains(v, "dev  (2: h2, h3)") || !strings.Contains(v, "prod  (1: h1)") {
		t.Fatalf("expanded:\n%s", v)
	}
	m = send(m, key("enter"), key("up"))
	if strings.Contains(m.View(), "h2, h3") || m.labelFocus != 2 {
		t.Fatalf("collapse/leave: focus=%d", m.labelFocus)
	}
}

func TestProfileDefaultKey(t *testing.T) {
	store := profiles.NewStore(filepath.Join(t.TempDir(), "p.json"))
	store.Save("", profiles.Profile{Name: "a", Server: "s", APIKey: "k"})
	store.Save("", profiles.Profile{Name: "b", Server: "s", APIKey: "k"})
	m := New(nil, "", "", "").WithProfiles(store, "")
	m = send(m, key("down"), key("s"))
	if m.defaultName != "b" || !strings.Contains(m.View(), "(default)") {
		t.Fatalf("default=%q\n%s", m.defaultName, m.View())
	}
	m = send(m, key("s"))
	if m.defaultName != "" {
		t.Fatalf("toggle should clear, got %q", m.defaultName)
	}
}
