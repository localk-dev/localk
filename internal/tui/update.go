package tui

import (
	"fmt"
	"strconv"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/localk-dev/localk/internal/devmode"
)

// Init kicks off the first runtime poll and starts the recurring
// tick. Both run in parallel via tea.Batch — first poll fires
// immediately so the user sees real status right away rather than
// after pollInterval has elapsed.
func (m *dashboardModel) Init() tea.Cmd {
	return tea.Batch(
		pollCmd(m.composePath, m.devPath, m.disablePath),
		scheduleTick(),
	)
}

// Update is the main event-loop dispatcher.
func (m *dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		// Schedule the next tick AND fire a poll. They run
		// concurrently — the next tick will arrive in pollInterval,
		// the poll result whenever it returns.
		return m, tea.Batch(pollCmd(m.composePath, m.devPath, m.disablePath), scheduleTick())

	case runtimeMsg:
		if msg.err != nil {
			m.statusErr = msg.err.Error()
		} else {
			m.statusErr = ""
			m.runtime = msg.state
			m.applyRuntimeToRows()
		}
		return m, nil

	case tea.KeyMsg:
		// Mode-specific handling first; falls through to normal mode
		// for unhandled keys.
		switch m.mode {
		case modeFilter:
			return m.updateFilterMode(msg)
		case modePortInput:
			return m.updatePortMode(msg)
		case modeConfirmQuit:
			return m.updateConfirmMode(msg)
		case modeHelp:
			return m.updateHelpMode(msg)
		}
		return m.updateNormalMode(msg)
	}
	return m, nil
}

// applyRuntimeToRows folds m.runtime into m.rows so View doesn't have
// to do a map lookup per row. Cheap, called only when a new poll
// arrives.
func (m *dashboardModel) applyRuntimeToRows() {
	for i := range m.rows {
		m.rows[i].Running = m.runtime[m.rows[i].Name]
	}
}

func (m *dashboardModel) updateNormalMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Any keystroke clears a transient flash; otherwise warnings
	// linger awkwardly between actions.
	m.flash = ""

	switch msg.String() {
	case "q":
		return m.attemptQuit()
	// ctrl+c is intercepted by the parent Model and turned into
	// tea.Quit before it reaches us, so no case here.

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
		}
		return m, nil
	case "g", "home":
		m.cursor = 0
		return m, nil
	case "G", "end":
		m.cursor = max(0, len(m.visible)-1)
		return m, nil

	case "d":
		m.toggleDisable()
		return m, nil
	case "e":
		m.openPortInput()
		return m, nil
	case "r":
		m.restoreFromDev()
		return m, nil

	case "/":
		m.mode = modeFilter
		m.filter.Focus()
		return m, textinput.Blink
	case "s":
		return m, m.save()
	case "?":
		m.mode = modeHelp
		return m, nil
	}
	return m, nil
}

func (m *dashboardModel) updateFilterMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filter.Blur()
		m.filter.SetValue("")
		m.applyFilter()
		m.mode = modeNormal
		return m, nil
	case "enter":
		m.filter.Blur()
		m.mode = modeNormal
		return m, nil
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.applyFilter()
	return m, cmd
}

func (m *dashboardModel) updatePortMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.cancelPortInput()
		return m, nil
	case "enter":
		m.confirmPortInput()
		return m, nil
	}
	var cmd tea.Cmd
	m.port, cmd = m.port.Update(msg)
	return m, cmd
}

func (m *dashboardModel) updateConfirmMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		// Save, then signal the parent Model that we want to leave
		// the dashboard and return to the menu. The parent reads
		// requestExit in its Update wrapper.
		m.mode = modeNormal
		cmd := m.save()
		m.requestExit = true
		return m, cmd
	case "n":
		// Discard pending changes; request exit to menu.
		m.pending = nil
		m.mode = modeNormal
		m.requestExit = true
		return m, nil
	case "esc":
		m.mode = modeNormal
		return m, nil
	}
	return m, nil
}

func (m *dashboardModel) updateHelpMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Any key dismisses help.
	_ = msg
	m.mode = modeNormal
	return m, nil
}

// attemptQuit handles the dashboard's `q` key. With unsaved changes
// it transitions to the confirm prompt; otherwise it signals the
// parent Model that the dashboard wants to leave (return to the
// top-level menu). Full-quit (the program exits) is the parent's
// job — triggered by ctrl+c, which the parent intercepts before
// the message reaches us.
func (m *dashboardModel) attemptQuit() (tea.Model, tea.Cmd) {
	if m.dirty() {
		m.mode = modeConfirmQuit
		return m, nil
	}
	m.requestExit = true
	return m, nil
}

// toggleDisable flips the disabled flag on the current row, with the
// dev-mode conflict guard. Mirrors `localk disable`'s behavior.
func (m *dashboardModel) toggleDisable() {
	row := m.currentRow()
	if row == nil {
		return
	}
	if row.DevPort != 0 {
		m.flash = fmt.Sprintf("can't disable %q — currently in dev mode (press `r` to restore first)", row.Name)
		return
	}
	if row.Disabled {
		row.Disabled = false
		m.pending = append(m.pending, dirtyOp{kind: disableToggleOff, service: row.Name})
	} else {
		row.Disabled = true
		m.pending = append(m.pending, dirtyOp{kind: disableToggleOn, service: row.Name})
	}
}

// openPortInput moves into modePortInput for the current row. If the
// row is already in dev mode the user should `r` to restore first;
// re-entering dev would silently overwrite the existing port.
func (m *dashboardModel) openPortInput() {
	row := m.currentRow()
	if row == nil {
		return
	}
	if row.Disabled {
		m.flash = fmt.Sprintf("can't enter dev mode on %q — currently disabled (press `d` to re-enable first)", row.Name)
		return
	}
	if row.DevPort != 0 {
		m.flash = fmt.Sprintf("%q is already in dev mode on port %d (press `r` to restore)", row.Name, row.DevPort)
		return
	}
	m.pendingDevService = row.Name
	m.port.SetValue("")
	m.port.Focus()
	m.mode = modePortInput
}

func (m *dashboardModel) cancelPortInput() {
	m.pendingDevService = ""
	m.port.Blur()
	m.port.SetValue("")
	m.mode = modeNormal
}

func (m *dashboardModel) confirmPortInput() {
	v := m.port.Value()
	port, err := strconv.Atoi(v)
	if err != nil || port <= 0 || port > 65535 {
		m.flash = fmt.Sprintf("invalid port %q (need a number between 1 and 65535)", v)
		m.cancelPortInput()
		return
	}
	// Find the row again — cursor might've moved if the user navigated
	// away with the modal up. We pinned the service name in
	// pendingDevService specifically for this reason.
	for i := range m.rows {
		if m.rows[i].Name == m.pendingDevService {
			m.rows[i].DevPort = port
			m.pending = append(m.pending, dirtyOp{kind: devEnter, service: m.pendingDevService, port: port})
			break
		}
	}
	m.cancelPortInput()
}

func (m *dashboardModel) restoreFromDev() {
	row := m.currentRow()
	if row == nil {
		return
	}
	if row.DevPort == 0 {
		m.flash = fmt.Sprintf("%q is not in dev mode", row.Name)
		return
	}
	row.DevPort = 0
	m.pending = append(m.pending, dirtyOp{kind: devLeave, service: row.Name})
}

// save flushes pending changes to both overlays. Returns a Cmd
// (tea.Cmd) that emits a saveResultMsg when done — but we keep it
// synchronous for v1 since the writes are tiny YAML files. The
// returned Cmd is nil-safe.
func (m *dashboardModel) save() tea.Cmd {
	if !m.dirty() {
		m.flash = "nothing to save"
		return nil
	}

	dev, _, err := devmode.Load(m.devPath)
	if err != nil {
		m.flash = fmt.Sprintf("save: %v", err)
		return nil
	}
	disabled, _, err := devmode.LoadDisabled(m.disablePath)
	if err != nil {
		m.flash = fmt.Sprintf("save: %v", err)
		return nil
	}

	// Replay pending ops in order. This makes "disable then re-enable
	// in the same session" cancel out cleanly.
	for _, op := range m.pending {
		switch op.kind {
		case disableToggleOn:
			disabled.Add(op.service)
		case disableToggleOff:
			disabled.Remove(op.service)
		case devEnter:
			containerPort := devmode.ContainerPortFor(m.base.Services[op.service].Ports, 80)
			dev.AddProxy(op.service, containerPort, op.port)
		case devLeave:
			dev.RemoveProxy(op.service)
		}
	}

	if err := dev.Save(m.devPath); err != nil {
		m.flash = fmt.Sprintf("save dev overlay: %v", err)
		return nil
	}
	if err := disabled.Save(m.disablePath); err != nil {
		m.flash = fmt.Sprintf("save disable overlay: %v", err)
		return nil
	}

	count := len(m.pending)
	m.pending = nil
	m.flash = fmt.Sprintf("saved %d change(s); run `localk up` to apply", count)
	return nil
}
