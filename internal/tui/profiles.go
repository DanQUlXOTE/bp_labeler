package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bplabeler/internal/bpclient"
	"bplabeler/internal/profiles"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type pingMsg struct{ err error }

// DefaultServer prefills the server field of a new profile.
const DefaultServer = "https://app.bindplane.com"

// WithProfiles enables the profile manager. When there is no connected
// client yet, the program starts on the profile list.
func (m Model) WithProfiles(store *profiles.Store, active string) Model {
	m.store, m.active = store, active
	m.reloadProfiles()
	if m.client == nil {
		m.stage = stProfiles
	}
	return m
}

func (m *Model) reloadProfiles() {
	ps, err := m.store.List()
	if err != nil {
		m.msg = "profiles: " + err.Error()
	}
	m.profs = ps
	m.defaultName, _ = m.store.Default()
	if m.profCur >= len(ps) {
		m.profCur = max(0, len(ps)-1)
	}
}

func newProfileInputs() (name, server, key textinput.Model) {
	mk := func(ph string) textinput.Model {
		t := textinput.New()
		t.Placeholder = ph
		t.Width = 60
		return t
	}
	name, server, key = mk("e.g. acme-prod"), mk("https://app.bindplane.com"), mk("Bindplane API key")
	server.SetValue(DefaultServer)
	key.EchoMode = textinput.EchoPassword
	key.EchoCharacter = '*'
	return
}

// useProfile connects with p and starts fresh on the search screen.
func (m Model) useProfile(p profiles.Profile) (tea.Model, tea.Cmd) {
	c, err := bpclient.New(bpclient.Options{BaseURL: p.Server, APIKey: p.APIKey, SkipTLSVerify: p.SkipTLS})
	if err != nil {
		m.msg = err.Error()
		return m, nil
	}
	m.client, m.active = c, p.Name
	m.agents, m.selected, m.cursor, m.top = nil, map[string]bool{}, 0, 0
	m.fleets, m.fleetsLoaded, m.versions, m.versionsOK = nil, false, nil, false
	m.fleet, m.up, m.plan = nil, nil, nil
	m.stage, m.msg, m.note = stSearch, "", ""
	m.setEditing(false)
	return m, nil
}

func (m Model) updateProfiles(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.profDelete {
		m.profDelete = false
		if k.String() == "y" && len(m.profs) > 0 {
			name := m.profs[m.profCur].Name
			if err := m.store.Delete(name); err != nil {
				m.msg = err.Error()
			} else {
				m.note = fmt.Sprintf("deleted %q", name)
				if strings.EqualFold(name, m.active) {
					m.client, m.active = nil, ""
				}
				m.reloadProfiles()
			}
		} else {
			m.note = "delete cancelled"
		}
		return m, nil
	}
	m.msg, m.note = "", ""
	switch k.String() {
	case "esc", "q":
		if m.client == nil {
			return m, tea.Quit
		}
		m.stage, m.note = stSearch, ""
	case "up", "k":
		m.profCur = max(0, m.profCur-1)
	case "down", "j":
		m.profCur = min(len(m.profs)-1, m.profCur+1)
	case "enter":
		if len(m.profs) > 0 {
			return m.useProfile(m.profs[m.profCur])
		}
	case "a":
		return m.openForm(-1)
	case "e":
		if len(m.profs) > 0 {
			return m.openForm(m.profCur)
		}
	case "d":
		if len(m.profs) > 0 {
			m.profDelete = true
		}
	case "s":
		if len(m.profs) > 0 {
			name := m.profs[m.profCur].Name
			if strings.EqualFold(name, m.defaultName) {
				name = ""
			}
			if err := m.store.SetDefault(name); err != nil {
				m.msg = err.Error()
			} else if name == "" {
				m.note = "default profile cleared"
			} else {
				m.note = fmt.Sprintf("%q is now the default (used on startup)", name)
			}
			m.reloadProfiles()
		}
	}
	return m, nil
}

// openForm starts the add (idx -1) or edit form.
func (m Model) openForm(idx int) (tea.Model, tea.Cmd) {
	m.pName, m.pServer, m.pKey = newProfileInputs()
	m.pIdx, m.pFocus, m.pEditing, m.pTLS = idx, 0, false, false
	if idx >= 0 {
		p := m.profs[idx]
		m.pName.SetValue(p.Name)
		m.pServer.SetValue(p.Server)
		m.pKey.Placeholder = "(unchanged: " + profiles.Mask(p.APIKey) + ")"
		m.pTLS = p.SkipTLS
	}
	m.stage, m.msg, m.note = stProfileForm, "", ""
	return m, nil
}

func (m *Model) formEdit(on bool) {
	m.pEditing = on
	m.pName.Blur()
	m.pServer.Blur()
	m.pKey.Blur()
	if !on {
		return
	}
	switch m.pFocus {
	case 0:
		m.pName.Focus()
	case 1:
		m.pServer.Focus()
	case 2:
		m.pKey.Focus()
	}
}

// formProfile builds the profile being edited, keeping the stored key when
// the key line was left empty.
func (m Model) formProfile() (old string, p profiles.Profile) {
	p = profiles.Profile{Name: m.pName.Value(), Server: m.pServer.Value(), APIKey: m.pKey.Value(), SkipTLS: m.pTLS}
	if m.pIdx >= 0 {
		old = m.profs[m.pIdx].Name
		if strings.TrimSpace(p.APIKey) == "" {
			p.APIKey = m.profs[m.pIdx].APIKey
		}
	}
	return
}

func (m Model) updateProfileForm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pEditing {
		switch k.String() {
		case "esc", "enter":
			m.formEdit(false)
			return m, nil
		}
		var cmd tea.Cmd
		switch m.pFocus {
		case 0:
			m.pName, cmd = m.pName.Update(k)
		case 1:
			m.pServer, cmd = m.pServer.Update(k)
		default:
			m.pKey, cmd = m.pKey.Update(k)
		}
		return m, cmd
	}
	switch k.String() {
	case "esc":
		m.stage, m.msg, m.note = stProfiles, "", ""
	case "down", "tab", "j":
		m.pFocus = (m.pFocus + 1) % 4
	case "up", "shift+tab", "k":
		m.pFocus = (m.pFocus + 3) % 4
	case "enter", "i":
		if m.pFocus == 3 {
			m.pTLS = !m.pTLS
			return m, nil
		}
		m.formEdit(true)
		return m, textinput.Blink
	case "s":
		old, p := m.formProfile()
		if err := m.store.Save(old, p); err != nil {
			m.msg, m.note = err.Error(), ""
			return m, nil
		}
		m.reloadProfiles()
		for i, e := range m.profs {
			if strings.EqualFold(e.Name, p.Name) {
				m.profCur = i
			}
		}
		if old != "" && strings.EqualFold(old, m.active) {
			m.active = p.Name // renamed; reconnect with updated key on next select
		}
		m.stage, m.msg, m.note = stProfiles, "", fmt.Sprintf("saved %q (enter to use it)", p.Name)
	case "t":
		_, p := m.formProfile()
		c, err := bpclient.New(bpclient.Options{BaseURL: p.Server, APIKey: p.APIKey, SkipTLSVerify: p.SkipTLS, Timeout: 15 * time.Second})
		if err != nil {
			m.msg, m.note = err.Error(), ""
			return m, nil
		}
		m.msg, m.note = "", "testing connection..."
		return m, func() tea.Msg { return pingMsg{c.Ping(context.Background())} }
	}
	return m, nil
}

func (m Model) viewProfiles(b *strings.Builder) {
	fmt.Fprintf(b, "Profiles %s\n\n", dim.Render("("+m.store.Path()+")"))
	if len(m.profs) == 0 {
		b.WriteString(dim.Render("No profiles yet. Press a to add one.") + "\n")
	}
	for i, p := range m.profs {
		star := "  "
		if strings.EqualFold(p.Name, m.active) {
			star = "* "
		}
		def := ""
		if strings.EqualFold(p.Name, m.defaultName) {
			def = "  (default)"
		}
		line := fmt.Sprintf("%s%-24s %-40s %s%s", star, trunc(p.Name, 24), trunc(p.Server, 40), profiles.Mask(p.APIKey), def)
		if i == m.profCur {
			line = cur.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	if m.profDelete && len(m.profs) > 0 {
		b.WriteString(bad.Render(fmt.Sprintf("Delete profile %q? press y to confirm, any other key cancels", m.profs[m.profCur].Name)) + "\n")
		return
	}
	b.WriteString(dim.Render("up/down move  enter use  s set/clear default  a add  e edit  d delete  esc back  (* = connected)") + "\n")
}

func (m Model) viewProfileForm(b *strings.Builder) {
	verb := "Add profile"
	if m.pIdx >= 0 {
		verb = "Edit profile"
	}
	b.WriteString(verb + "\n\n")
	mark := [4]string{"  ", "  ", "  ", "  "}
	mark[m.pFocus] = "> "
	if m.pEditing {
		mark[m.pFocus] = "* "
	}
	tls := "no"
	if m.pTLS {
		tls = "yes (lab only)"
	}
	fmt.Fprintf(b, "%sName:        %s\n", mark[0], m.pName.View())
	fmt.Fprintf(b, "%sServer:      %s\n", mark[1], m.pServer.View())
	fmt.Fprintf(b, "%sAPI key:     %s\n", mark[2], m.pKey.View())
	fmt.Fprintf(b, "%sSkip TLS:    %s\n\n", mark[3], tls)
	if m.pEditing {
		b.WriteString(dim.Render("INPUT  enter/esc done") + "\n")
	} else {
		b.WriteString(dim.Render("up/down switch line  enter edit/toggle  t test connection  s save  esc cancel") + "\n")
	}
}
