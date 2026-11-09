// Package tui is the interactive search, select and label flow.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"bplabeler/internal/bpclient"
	"bplabeler/internal/plan"
	"bplabeler/internal/profiles"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type stage int

const (
	stSearch stage = iota
	stLoading
	stList
	stLabels
	stFleet
	stVersion
	stProfiles
	stProfileForm
	stPreview
	stConfirm
	stApplying
	stDone
)

var (
	title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	dim   = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	warn  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	bad   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	good  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	cur   = lipgloss.NewStyle().Reverse(true)
)

type agentsMsg struct {
	agents []bpclient.Agent
	err    error
}
type fleetsMsg struct {
	fleets []bpclient.Fleet
	err    error
}
type versionsMsg struct {
	versions []bpclient.AgentVersion
	err      error
}
type upgradedMsg struct{ results []plan.UpgradeResult }
type appliedMsg struct{ results []plan.Result }

type Model struct {
	client *bpclient.Client

	stage    stage
	query    textinput.Model
	selector textinput.Model
	labels   textinput.Model
	remove   textinput.Model
	confirm  textinput.Model
	fleetFlt textinput.Model

	width, rows  int // terminal size
	listH        int // collector rows visible
	detailH      int // label lines visible in the detail pane
	detailTop    int
	ver          textinput.Model // search: filter by current version
	verFlt       textinput.Model // picker: filter available versions
	versions     []bpclient.AgentVersion
	versionsOK   bool
	verBusy      bool
	verView      []bpclient.AgentVersion
	verCur       int
	up           *plan.UpgradePlan // non-nil: the preview/confirm/done flow is an upgrade
	upResults    []plan.UpgradeResult
	prevTop      int
	store        *profiles.Store
	active       string // connected profile name
	defaultName  string // profile used on startup
	profs        []profiles.Profile
	profCur      int
	profDelete   bool
	pName        textinput.Model
	pServer      textinput.Model
	pKey         textinput.Model
	pTLS         bool
	pFocus       int
	pIdx         int // index being edited, -1 when adding
	pEditing     bool
	note         string          // green status text
	paneCur      int             // key row selected in the labels pane (focus 3)
	expanded     map[string]bool // label keys expanded in the labels pane
	editing      bool            // a text line is in input mode
	labelFocus   int             // 0 labels, 1 remove, 2 fleet line
	fleets       []bpclient.Fleet
	fleetsLoaded bool
	fleetsBusy   bool
	fleetView    []bpclient.Fleet // fleets after filtering
	fleetCur     int
	fleet        *bpclient.Fleet
	focus        int // 0 query, 1 selector

	agents   []bpclient.Agent
	selected map[string]bool
	cursor   int
	top      int
	height   int

	plan    *plan.Plan
	results []plan.Result
	msg     string
}

func New(c *bpclient.Client, query, selector, version string) Model {
	mk := func(ph, val string) textinput.Model {
		t := textinput.New()
		t.Placeholder = ph
		t.SetValue(val)
		t.Width = 60
		t.Prompt = ""
		return t
	}
	m := Model{
		client:   c,
		query:    mk("e.g. os:linux or hostname:web (empty = all)", query),
		selector: mk("e.g. env=prod,team!=db (optional)", selector),
		labels:   mk("env=prod,team=db   (key= removes a label)", ""),
		ver:      mk("e.g. 1.60.0 or 1.60 (optional)", version),
		verFlt:   mk("filter versions", ""),
		confirm:  mk("type confirm", ""),
		remove:   mk("keys to remove, e.g. team,tier", ""),
		fleetFlt: mk("filter fleets by name", ""),
		selected: map[string]bool{},
		expanded: map[string]bool{},
		height:   20,
		listH:    10,
		detailH:  5,
		width:    120,
	}
	return m
}

func (m Model) Init() tea.Cmd { return textinput.Blink }

func (m Model) load() tea.Cmd {
	q, s, v := m.query.Value(), m.selector.Value(), m.ver.Value()
	return func() tea.Msg {
		a, err := m.client.ListAgents(context.Background(), q, s)
		return agentsMsg{plan.FilterVersion(a, v), err}
	}
}

func (m Model) run() tea.Cmd {
	p := m.plan
	return func() tea.Msg { return appliedMsg{p.Execute(context.Background(), m.client)} }
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if h := msg.Height - 9; h > 3 {
			m.height = h
		}
		m.width, m.rows = msg.Width, msg.Height
		m.detailH = max(3, msg.Height/4)
		// title(2) count(2) help(2) msg(2) + detail (rule, info, labels, more)
		m.listH = max(3, msg.Height-8-(m.detailH+3))
	case agentsMsg:
		if msg.err != nil {
			m.stage, m.msg = stSearch, "search failed: "+msg.err.Error()
			return m, nil
		}
		m.agents, m.selected, m.cursor, m.top, m.msg = msg.agents, map[string]bool{}, 0, 0, ""
		m.stage = stList
		return m, nil
	case fleetsMsg:
		m.fleetsBusy = false
		if msg.err != nil {
			m.stage, m.msg = stLabels, "fleet lookup failed: "+msg.err.Error()
			return m, nil
		}
		m.fleets, m.fleetsLoaded = msg.fleets, true
		m.refilter()
		return m, nil
	case pingMsg:
		if msg.err != nil {
			m.msg, m.note = "connection failed: "+msg.err.Error(), ""
		} else {
			m.msg, m.note = "", "connection OK"
		}
		return m, nil
	case versionsMsg:
		m.verBusy = false
		if msg.err != nil {
			m.stage, m.msg = stList, "version lookup failed: "+msg.err.Error()
			return m, nil
		}
		plan.SortVersions(msg.versions)
		m.versions, m.versionsOK = msg.versions, true
		m.refilterVer()
		return m, nil
	case upgradedMsg:
		m.upResults, m.stage = msg.results, stDone
		return m, nil
	case appliedMsg:
		m.results, m.stage = msg.results, stDone
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.stage {
		case stSearch:
			return m.updateSearch(msg)
		case stList:
			return m.updateList(msg)
		case stLabels:
			return m.updateLabels(msg)
		case stFleet:
			return m.updateFleet(msg)
		case stVersion:
			return m.updateVersion(msg)
		case stProfiles:
			return m.updateProfiles(msg)
		case stProfileForm:
			return m.updateProfileForm(msg)
		case stPreview:
			return m.updatePreview(msg)
		case stConfirm:
			return m.updateConfirm(msg)
		case stDone:
			if msg.String() == "q" || msg.String() == "enter" {
				return m, tea.Quit
			}
			if msg.String() == "n" {
				m.stage, m.msg, m.up = stSearch, "", nil
				m.setEditing(false)
				return m, nil
			}
		}
	}
	return m, nil
}

func (m *Model) setEditing(on bool) {
	m.editing = on
	m.query.Blur()
	m.selector.Blur()
	m.ver.Blur()
	m.labels.Blur()
	m.remove.Blur()
	if !on {
		return
	}
	switch {
	case m.stage == stSearch && m.focus == 0:
		m.query.Focus()
	case m.stage == stSearch && m.focus == 1:
		m.selector.Focus()
	case m.stage == stSearch:
		m.ver.Focus()
	case m.stage == stLabels && m.labelFocus == 0:
		m.labels.Focus()
	case m.stage == stLabels:
		m.remove.Focus()
	}
}

func (m Model) updateSearch(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.editing {
		switch k.String() {
		case "esc", "enter":
			m.setEditing(false)
			return m, nil
		}
		var cmd tea.Cmd
		switch m.focus {
		case 0:
			m.query, cmd = m.query.Update(k)
		case 1:
			m.selector, cmd = m.selector.Update(k)
		default:
			m.ver, cmd = m.ver.Update(k)
		}
		return m, cmd
	}
	switch k.String() {
	case "esc", "q":
		return m, tea.Quit
	case "tab", "down", "j":
		m.focus = (m.focus + 1) % 3
	case "shift+tab", "up", "k":
		m.focus = (m.focus + 2) % 3
	case "enter", "i":
		m.setEditing(true)
		return m, textinput.Blink
	case "s":
		m.stage, m.msg = stLoading, ""
		return m, m.load()
	case "p":
		if m.store != nil {
			m.reloadProfiles()
			m.stage, m.msg, m.note = stProfiles, "", ""
		}
	}
	return m, nil
}

func (m Model) updateList(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.agents)
	prev := m.cursor
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "esc", "/":
		m.stage, m.msg = stSearch, ""
		m.setEditing(false)
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "pgup":
		m.cursor = max(0, m.cursor-m.listH)
	case "pgdown":
		m.cursor = min(n-1, m.cursor+m.listH)
	case " ":
		if n > 0 {
			id := m.agents[m.cursor].ID
			m.selected[id] = !m.selected[id]
			if !m.selected[id] {
				delete(m.selected, id)
			}
		}
	case "a":
		for _, a := range m.agents {
			m.selected[a.ID] = true
		}
	case "n":
		m.selected = map[string]bool{}
	case "u":
		if len(m.selected) == 0 {
			m.msg = "select at least one collector with space (or a for all), then press u to upgrade"
			return m, nil
		}
		m.msg = ""
		return m.openVersions()
	case "]":
		if n == 0 {
			break
		}
		if a := m.agents[m.cursor]; m.detailTop < len(a.Labels)-m.detailH {
			m.detailTop++
		}
	case "[":
		m.detailTop = max(0, m.detailTop-1)
	case "enter":
		if len(m.selected) == 0 {
			m.msg = "select at least one collector with space (or a for all)"
			return m, nil
		}
		m.msg, m.stage, m.labelFocus, m.up, m.paneCur, m.expanded = "", stLabels, 0, nil, 0, map[string]bool{}
		m.setEditing(false)
		return m, nil
	}
	if m.cursor != prev {
		m.detailTop = 0
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+m.listH {
		m.top = m.cursor - m.listH + 1
	}
	return m, nil
}

func (m Model) updateLabels(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.editing {
		switch k.String() {
		case "esc", "enter":
			m.setEditing(false)
			return m, nil
		}
		var cmd tea.Cmd
		if m.labelFocus == 0 {
			m.labels, cmd = m.labels.Update(k)
		} else {
			m.remove, cmd = m.remove.Update(k)
		}
		return m, cmd
	}
	switch k.String() {
	case "esc":
		m.stage, m.msg = stList, ""
	case "tab":
		m.labelFocus = (m.labelFocus + 1) % 4
	case "shift+tab":
		m.labelFocus = (m.labelFocus + 3) % 4
	case "down", "j":
		if m.labelFocus == 3 {
			m.paneCur = min(m.paneCur+1, max(0, len(m.selectionLabels())-1))
		} else {
			m.labelFocus++
		}
	case "up", "k":
		switch {
		case m.labelFocus == 3 && m.paneCur > 0:
			m.paneCur--
		case m.labelFocus == 0:
			m.labelFocus = 3
		default:
			m.labelFocus--
		}
	case "enter", "i":
		if m.labelFocus == 3 {
			if rows := m.selectionLabels(); m.paneCur < len(rows) {
				k := rows[m.paneCur].key
				m.expanded[k] = !m.expanded[k]
			}
			return m, nil
		}
		if m.labelFocus == 2 {
			return m.openFleets()
		}
		m.setEditing(true)
		return m, textinput.Blink
	case "v":
		return m.preview()
	case "x", "backspace", "delete":
		switch m.labelFocus {
		case 1:
			m.remove.SetValue("")
		case 2:
			m.fleet = nil
		}
	}
	return m, nil
}

// removalKeys parses the Remove line: comma separated label keys.
func removalKeys(s string) []string {
	var out []string
	for _, k := range strings.Split(s, ",") {
		if k = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(k), "-")); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func (m Model) preview() (tea.Model, tea.Cmd) {
	var want map[string]string
	if strings.TrimSpace(m.labels.Value()) != "" {
		var err error
		if want, err = plan.ParseLabels(m.labels.Value()); err != nil {
			m.msg = err.Error()
			return m, nil
		}
	}
	for _, k := range removalKeys(m.remove.Value()) {
		if v, dup := want[k]; dup && v != "" {
			m.msg = fmt.Sprintf("label %q is both set and removed", k)
			return m, nil
		}
		if k == plan.FleetKey && m.fleet != nil {
			m.msg = "can't choose a fleet and remove the fleet label together"
			return m, nil
		}
		if want == nil {
			want = map[string]string{}
		}
		want[k] = ""
	}
	if len(want) == 0 && m.fleet == nil {
		m.msg = "enter labels to set, labels to remove, a fleet, or any combination"
		return m, nil
	}
	var sel []bpclient.Agent
	for _, a := range m.agents {
		if m.selected[a.ID] {
			sel = append(sel, a)
		}
	}
	m.plan, m.stage, m.msg, m.up, m.prevTop = plan.ForAgents(sel, want, m.fleet), stPreview, "", nil, 0
	return m, nil
}

func (m Model) openVersions() (tea.Model, tea.Cmd) {
	m.stage, m.msg = stVersion, ""
	m.verFlt.SetValue("")
	m.verFlt.Focus()
	if m.versionsOK {
		m.refilterVer()
		return m, textinput.Blink
	}
	m.verBusy = true
	c := m.client
	return m, func() tea.Msg {
		v, err := c.ListAgentVersions(context.Background())
		return versionsMsg{v, err}
	}
}

// refilterVer narrows the available versions to the agent types in the
// selection (when any match) and to the text filter.
func (m *Model) refilterVer() {
	types := map[string]bool{}
	for _, a := range m.selectedAgents() {
		types[a.Type] = true
	}
	scoped := m.versions[:0:0]
	for _, v := range m.versions {
		if types[v.Type] {
			scoped = append(scoped, v)
		}
	}
	if len(scoped) == 0 {
		scoped = m.versions
	}
	needle := strings.ToLower(strings.TrimSpace(m.verFlt.Value()))
	m.verView = m.verView[:0:0]
	for _, v := range scoped {
		if needle == "" || strings.Contains(strings.ToLower(v.Version), needle) {
			m.verView = append(m.verView, v)
		}
	}
	if m.verCur >= len(m.verView) {
		m.verCur = max(0, len(m.verView)-1)
	}
}

func (m Model) updateVersion(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.stage = stList
		return m, nil
	case "up", "ctrl+p":
		m.verCur = max(0, m.verCur-1)
		return m, nil
	case "down", "ctrl+n":
		m.verCur = min(len(m.verView)-1, m.verCur+1)
		return m, nil
	case "enter":
		if len(m.verView) == 0 {
			return m, nil
		}
		m.up = plan.ForUpgrade(m.selectedAgents(), m.verView[m.verCur].Version)
		m.stage, m.prevTop = stPreview, 0
		return m, nil
	}
	var cmd tea.Cmd
	m.verFlt, cmd = m.verFlt.Update(k)
	m.refilterVer()
	return m, cmd
}

func (m Model) openFleets() (tea.Model, tea.Cmd) {
	m.stage, m.msg = stFleet, ""
	m.fleetFlt.SetValue("")
	m.fleetFlt.Focus()
	if m.fleetsLoaded {
		m.refilter()
		return m, textinput.Blink
	}
	m.fleetsBusy = true
	c := m.client
	return m, func() tea.Msg {
		f, err := c.ListFleets(context.Background())
		return fleetsMsg{f, err}
	}
}

func (m *Model) refilter() {
	needle := strings.ToLower(strings.TrimSpace(m.fleetFlt.Value()))
	m.fleetView = m.fleetView[:0:0]
	for _, f := range m.fleets {
		if needle == "" || strings.Contains(strings.ToLower(f.Title()), needle) || strings.Contains(strings.ToLower(f.Name), needle) {
			m.fleetView = append(m.fleetView, f)
		}
	}
	if m.fleetCur >= len(m.fleetView) {
		m.fleetCur = max(0, len(m.fleetView)-1)
	}
}

func (m Model) updateFleet(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.stage = stLabels
		return m, nil
	case "up", "ctrl+p":
		if m.fleetCur > 0 {
			m.fleetCur--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.fleetCur < len(m.fleetView)-1 {
			m.fleetCur++
		}
		return m, nil
	case "enter":
		if len(m.fleetView) == 0 {
			return m, nil
		}
		f := m.fleetView[m.fleetCur]
		m.fleet, m.stage, m.labelFocus = &f, stLabels, 0
		m.setEditing(false)
		return m, nil
	}
	var cmd tea.Cmd
	m.fleetFlt, cmd = m.fleetFlt.Update(k)
	m.refilter()
	return m, cmd
}

func (m Model) selectedAgents() []bpclient.Agent {
	var sel []bpclient.Agent
	for _, a := range m.agents {
		if m.selected[a.ID] {
			sel = append(sel, a)
		}
	}
	return sel
}

func (m Model) pageH() int {
	if m.rows == 0 {
		return 20
	}
	return max(5, m.rows-9)
}

// previewLines renders the pending change report (label or upgrade).
func (m Model) previewLines() []string {
	var sb strings.Builder
	if m.up != nil {
		m.up.Render(&sb)
	} else {
		m.plan.Render(&sb)
	}
	return strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
}

func (m Model) pending() int {
	if m.up != nil {
		return m.up.Pending()
	}
	return m.plan.Pending()
}

func (m Model) updatePreview(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "b":
		if m.up != nil {
			m.stage = stVersion
		} else {
			m.stage = stLabels
		}
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.prevTop = max(0, m.prevTop-1)
	case "down", "j":
		m.prevTop++
	case "pgup":
		m.prevTop = max(0, m.prevTop-m.pageH())
	case "pgdown":
		m.prevTop += m.pageH()
	case "a":
		if m.pending() == 0 {
			m.msg = "nothing to change"
		} else {
			m.msg, m.stage = "", stConfirm
			m.confirm.SetValue("")
			m.confirm.Focus()
			return m, textinput.Blink
		}
	}
	m.prevTop = min(m.prevTop, max(0, len(m.previewLines())-m.pageH()))
	return m, nil
}

func (m Model) updateConfirm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.stage, m.msg = stPreview, ""
		return m, nil
	case "enter":
		if strings.TrimSpace(m.confirm.Value()) != "confirm" {
			m.msg = `type "confirm" exactly to apply, or esc to go back`
			return m, nil
		}
		m.msg, m.stage = "", stApplying
		if m.up != nil {
			up, c := m.up, m.client
			return m, func() tea.Msg { return upgradedMsg{up.Execute(context.Background(), c)} }
		}
		return m, m.run()
	}
	var cmd tea.Cmd
	m.confirm, cmd = m.confirm.Update(k)
	return m, cmd
}

func (m Model) View() string {
	var b strings.Builder
	mode := ""
	if m.stage == stConfirm || m.stage == stApplying || m.stage == stDone {
		mode = bad.Render("APPLY")
	}
	prof := ""
	if m.active != "" {
		prof = dim.Render("[" + m.active + "]")
	}
	fmt.Fprintf(&b, "%s  %s %s\n\n", title.Render("Bindplane collector labeler"), prof, mode)

	switch m.stage {
	case stSearch:
		b.WriteString(m.cursorMark(0) + "Query:    " + m.query.View() + "\n")
		b.WriteString(m.cursorMark(1) + "Selector: " + m.selector.View() + "\n")
		b.WriteString(m.cursorMark(2) + "Version:  " + m.ver.View() + "\n\n")
		if m.editing {
			b.WriteString(dim.Render("INPUT  enter/esc done") + "\n")
		} else {
			b.WriteString(dim.Render("up/down switch line  enter edit line  s search  p profiles  esc quit") + "\n")
		}
	case stLoading:
		b.WriteString("Searching...\n")
	case stList:
		fmt.Fprintf(&b, "%d collector(s), %d selected\n\n", len(m.agents), len(m.selected))
		end := min(len(m.agents), m.top+m.listH)
		for i := m.top; i < end; i++ {
			a := m.agents[i]
			mark := "[ ]"
			if m.selected[a.ID] {
				mark = "[x]"
			}
			state := "offline"
			if a.Connected {
				state = "online "
			}
			head := fmt.Sprintf("%s %-28s %s  %-26s  ", mark, trunc(host(a), 28), state, trunc(a.ID, 26))
			line := head + trunc(labelPlain(a.Labels), max(10, m.width-len(head)-1))
			switch {
			case i == m.cursor:
				line = cur.Render(line)
			case !a.Connected:
				line = dim.Render(line)
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
		m.writeDetail(&b)
		b.WriteString(dim.Render("space toggle  a all  n none  [ ] scroll labels  enter label  u upgrade  / new search  q quit") + "\n")
	case stLabels:
		fleetTxt := dim.Render("(none) enter to choose")
		if m.fleet != nil {
			fleetTxt = good.Render(m.fleet.Title()) + dim.Render(" ("+m.fleet.Name+")")
		}
		mark := [4]string{"  ", "  ", "  ", "  "}
		mark[m.labelFocus] = "> "
		if m.editing {
			mark[m.labelFocus] = "* "
		}
		fmt.Fprintf(&b, "Changes for %d collector(s):\n\n", len(m.selected))
		fmt.Fprintf(&b, "%sSet:    %s\n", mark[0], m.labels.View())
		fmt.Fprintf(&b, "%sRemove: %s\n", mark[1], m.remove.View())
		fmt.Fprintf(&b, "%sFleet:  %s\n\n", mark[2], fleetTxt)
		b.WriteString(dim.Render("The fleet label is set by choosing a fleet, not by typing fleet=...") + "\n\n")
		m.writeSelectionLabels(&b)
		b.WriteString("\n")
		if m.editing {
			b.WriteString(dim.Render("INPUT  enter/esc done") + "\n")
		} else if m.labelFocus == 3 {
			b.WriteString(dim.Render("up/down move  enter expand/collapse values and collectors  up from first row leaves the pane  v verify  esc back") + "\n")
		} else {
			b.WriteString(dim.Render("up/down switch line (down from Fleet enters the labels pane)  enter edit / pick fleet  x clear line  v verify (dry run)  esc back") + "\n")
		}
	case stProfiles:
		m.viewProfiles(&b)
	case stProfileForm:
		m.viewProfileForm(&b)
	case stFleet:
		b.WriteString("Choose a fleet:\n\n" + m.fleetFlt.View() + "\n\n")
		switch {
		case m.fleetsBusy:
			b.WriteString("Loading fleets...\n")
		case len(m.fleetView) == 0:
			b.WriteString(dim.Render("no fleets match") + "\n")
		default:
			fmt.Fprintf(&b, "%d of %d fleet(s)\n\n", len(m.fleetView), len(m.fleets))
			top := max(0, min(m.fleetCur-m.height/2, len(m.fleetView)-m.height))
			end := min(len(m.fleetView), top+m.height)
			for i := top; i < end; i++ {
				f := m.fleetView[i]
				line := fmt.Sprintf("  %-32s %s", trunc(f.Title(), 32), dim.Render(f.Configuration))
				if i == m.fleetCur {
					line = cur.Render(fmt.Sprintf("  %-32s %s", trunc(f.Title(), 32), f.Configuration))
				}
				b.WriteString(line + "\n")
			}
		}
		b.WriteString("\n" + dim.Render("type to filter  up/down move  enter select  esc cancel") + "\n")
	case stPreview:
		lines := m.previewLines()
		page := m.pageH()
		top := min(m.prevTop, max(0, len(lines)-page))
		end := min(len(lines), top+page)
		b.WriteString(strings.Join(lines[top:end], "\n") + "\n")
		if len(lines) > page {
			b.WriteString(dim.Render(fmt.Sprintf("\nlines %d-%d of %d", top+1, end, len(lines))) + "\n")
		}
		b.WriteString("\n" + dim.Render("dry run, nothing has been changed.  a apply  up/down pgup/pgdn scroll  b back  q quit") + "\n")
	case stVersion:
		b.WriteString(fmt.Sprintf("Upgrade %d collector(s) to:\n\n", len(m.selected)) + m.verFlt.View() + "\n\n")
		switch {
		case m.verBusy:
			b.WriteString("Loading available versions...\n")
		case len(m.verView) == 0:
			b.WriteString(dim.Render("no versions match") + "\n")
		default:
			fmt.Fprintf(&b, "%d version(s), newest first\n\n", len(m.verView))
			top := max(0, min(m.verCur-m.height/2, len(m.verView)-m.height))
			end := min(len(m.verView), top+m.height)
			for i := top; i < end; i++ {
				v := m.verView[i]
				tag := ""
				if v.Prerelease {
					tag = " (prerelease)"
				}
				line := fmt.Sprintf("  %-16s %s%s", v.Version, v.Type, tag)
				if i == m.verCur {
					line = cur.Render(line)
				}
				b.WriteString(line + "\n")
			}
		}
		b.WriteString("\n" + dim.Render("type to filter  up/down move  enter select (preview)  esc cancel") + "\n")
	case stConfirm:
		what := fmt.Sprintf("change labels on %d collector(s)", m.pending())
		if m.up != nil {
			what = fmt.Sprintf("upgrade %d collector(s) to %s", m.pending(), m.up.Target)
		}
		fmt.Fprintf(&b, "%s\n\nThis will %s in Bindplane.\nType %s to apply:\n\n%s\n\n", bad.Render("Apply changes?"), what, "confirm", m.confirm.View())
		b.WriteString(dim.Render("enter apply  esc cancel") + "\n")
	case stApplying:
		b.WriteString("Applying...\n")
	case stDone:
		for _, r := range m.upResults {
			if r.Err != nil {
				fmt.Fprintf(&b, "%s upgrade of %d collector(s): %v\n", bad.Render("FAILED"), r.Agents, r.Err)
				continue
			}
			fmt.Fprintf(&b, "%s upgrade to %s requested for %d collector(s)\n", good.Render("OK"), m.up.Target, r.Agents)
		}
		if m.up != nil {
			b.WriteString(dim.Render("Bindplane accepts the request and upgrades asynchronously; check the collectors for progress.") + "\n")
		}
		for _, r := range m.results {
			if r.Err != nil {
				fmt.Fprintf(&b, "%s %d collector(s): %v\n", bad.Render("FAILED"), r.Agents, r.Err)
				for _, id := range r.Failed {
					fmt.Fprintf(&b, "    %s\n", id)
				}
				continue
			}
			fmt.Fprintf(&b, "%s %d collector(s) updated\n", good.Render("OK"), r.Updated)
		}
		b.WriteString("\n" + dim.Render("n new search  q quit") + "\n")
	}
	if m.note != "" {
		b.WriteString("\n" + good.Render(m.note) + "\n")
	}
	if m.msg != "" {
		b.WriteString("\n" + bad.Render(m.msg) + "\n")
	}
	return b.String()
}

type labelInfo struct {
	key   string
	vals  map[string][]string // value -> collector names
	count int                 // collectors carrying the key
}

// selectionLabels summarises the labels on the selected collectors, by key.
func (m Model) selectionLabels() []labelInfo {
	idx := map[string]*labelInfo{}
	for _, a := range m.selectedAgents() {
		for k, v := range a.Labels {
			in := idx[k]
			if in == nil {
				in = &labelInfo{key: k, vals: map[string][]string{}}
				idx[k] = in
			}
			in.vals[v] = append(in.vals[v], host(a))
			in.count++
		}
	}
	out := make([]labelInfo, 0, len(idx))
	for _, in := range idx {
		out = append(out, *in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func names(hosts []string) string {
	const show = 3
	if len(hosts) <= show {
		return strings.Join(hosts, ", ")
	}
	return strings.Join(hosts[:show], ", ") + fmt.Sprintf(" +%d more", len(hosts)-show)
}

// writeSelectionLabels is the pane listing every label on the selected
// collectors. Focus it (down past the Fleet line), move with up/down, and
// press enter to expand a key into its values and the collectors holding each.
func (m Model) writeSelectionLabels(b *strings.Builder) {
	nSel := len(m.selected)
	rows := m.selectionLabels()
	w := max(20, m.width-1)
	b.WriteString(dim.Render(strings.Repeat("-", w)) + "\n")
	b.WriteString(title.Render(fmt.Sprintf("Current labels on %d selected collector(s): %d key(s)", nSel, len(rows))) + "\n")
	if len(rows) == 0 {
		b.WriteString(dim.Render("(no labels)") + "\n")
		return
	}

	type line struct {
		text string
		key  int // index into rows when this is a key line, else -1
	}
	var lines []line
	curLine := 0
	for i, in := range rows {
		open := m.expanded[in.key]
		sign := "[+]"
		if open {
			sign = "[-]"
		}
		var text string
		if len(in.vals) == 1 {
			for v := range in.vals {
				text = fmt.Sprintf("%s %s = %s", sign, in.key, v)
			}
		} else {
			text = fmt.Sprintf("%s %s  (%d different values)", sign, in.key, len(in.vals))
		}
		if in.count < nSel {
			text += fmt.Sprintf("  [on %d of %d]", in.count, nSel)
		}
		if i == m.paneCur {
			curLine = len(lines)
		}
		lines = append(lines, line{text, i})
		if open {
			vals := make([]string, 0, len(in.vals))
			for v := range in.vals {
				vals = append(vals, v)
			}
			sort.Slice(vals, func(a, c int) bool {
				if len(in.vals[vals[a]]) != len(in.vals[vals[c]]) {
					return len(in.vals[vals[a]]) > len(in.vals[vals[c]])
				}
				return vals[a] < vals[c]
			})
			for _, v := range vals {
				hs := in.vals[v]
				lines = append(lines, line{fmt.Sprintf("      %s  (%d: %s)", v, len(hs), names(hs)), -1})
			}
		}
	}

	page := 6
	if m.rows > 0 {
		page = max(4, m.rows-17)
	}
	top := max(0, min(curLine-page/2, len(lines)-page))
	end := min(len(lines), top+page)
	for _, l := range lines[top:end] {
		t := trunc("  "+l.text, w)
		if l.key == m.paneCur && m.labelFocus == 3 {
			t = cur.Render(t)
		}
		b.WriteString(t + "\n")
	}
	if len(lines) > page {
		b.WriteString(dim.Render(fmt.Sprintf("  rows %d-%d of %d", top+1, end, len(lines))) + "\n")
	}
}

func (m Model) cursorMark(i int) string {
	switch {
	case m.focus != i:
		return "  "
	case m.editing:
		return "* "
	}
	return "> "
}

func host(a bpclient.Agent) string {
	if a.HostName != "" {
		return a.HostName
	}
	return a.Name
}

func labelPlain(l map[string]string) string {
	if len(l) == 0 {
		return "-"
	}
	return labelStr(l)
}

// writeDetail renders the pane under the list: the focused collector's
// details and every label on its own line.
func (m Model) writeDetail(b *strings.Builder) {
	if len(m.agents) == 0 {
		return
	}
	a := m.agents[m.cursor]
	w := max(20, m.width-1)
	b.WriteString(dim.Render(strings.Repeat("-", w)) + "\n")
	state := "offline"
	if a.Connected {
		state = "online"
	}
	info := fmt.Sprintf("%s  id=%s  %s  %s %s  %s", host(a), a.ID, state, a.Type, a.Version, a.Platform)
	b.WriteString(title.Render(trunc(info, w)) + "\n")

	keys := make([]string, 0, len(a.Labels))
	for k := range a.Labels {
		keys = append(keys, k)
	}
	sortStrings(keys)
	if len(keys) == 0 {
		b.WriteString(dim.Render("(no labels)") + "\n")
	}
	top := min(m.detailTop, max(0, len(keys)-m.detailH))
	end := min(len(keys), top+m.detailH)
	for _, k := range keys[top:end] {
		b.WriteString(trunc("  "+k+" = "+a.Labels[k], w) + "\n")
	}
	if len(keys) > m.detailH {
		b.WriteString(dim.Render(fmt.Sprintf("  labels %d-%d of %d", top+1, end, len(keys))) + "\n")
	}
}

func labelStr(l map[string]string) string {
	if len(l) == 0 {
		return dim.Render("-")
	}
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sortStrings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + l[k]
	}
	return strings.Join(parts, ",")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "~"
}
