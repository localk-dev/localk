package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// Update is the top-level Bubble Tea handler. It does three things,
// in order:
//
//  1. Apply window-size updates to the parent Model and any sub-model
//     that needs to know its viewport size (the dashboard reads it
//     for divider widths).
//  2. Intercept ctrl+c globally — always quits the program, no matter
//     which screen is active. Lets sub-screens stay focused on their
//     own keys without having to remember to handle the universal
//     escape hatch.
//  3. Forward everything else to the active sub-screen's Update,
//     then check the per-screen "exit" / "chosen" / "dispatchAction"
//     signals to decide whether to switch screens.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = ws.Width
		m.height = ws.Height
		if m.dashboard != nil {
			m.dashboard.width = ws.Width
			m.dashboard.height = ws.Height
		}
		return m, nil
	}

	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "ctrl+c" {
		return m, tea.Quit
	}

	switch m.screen {
	case screenMenu:
		return m.updateMenu(msg)
	case screenDashboard:
		return m.updateDashboard(msg)
	case screenGenerate:
		return m.updateGenerate(msg)
	case screenAction:
		return m.updateAction(msg)
	}
	return m, nil
}

// updateMenu handles input on the top-level menu. The menu only
// reacts to KeyMsg; other messages (window size, ticks) are no-ops
// here. After dispatching to the menu's own Update we check the
// `chosen` signal to decide whether to switch screens.
func (m *Model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if k.String() == "q" {
		return m, tea.Quit
	}
	updated, cmd := m.menu.Update(k)
	m.menu = updated
	if m.menu.chosen == nil {
		return m, cmd
	}
	action := *m.menu.chosen
	m.menu.reset()
	return m.activate(action)
}

// activate switches to the screen the user picked from the menu.
// Centralizes the logic for screen entry so each menu item handler
// doesn't have to repeat init steps (lazy dashboard load, action
// dispatch for up/down).
func (m *Model) activate(a menuAction) (tea.Model, tea.Cmd) {
	switch a {
	case menuActionGenerate:
		m.generate.reset()
		m.screen = screenGenerate
		return m, nil
	case menuActionDashboard:
		// Lazy load — the compose file might not exist yet, in which
		// case we record the error and the dashboard view shows it
		// inline instead of refusing to launch the TUI.
		dm, err := newDashboardModel(m.outDir)
		if err != nil {
			m.dashboardErr = err
			m.dashboard = nil
			m.screen = screenDashboard
			return m, nil
		}
		m.dashboardErr = nil
		m.dashboard = dm
		m.dashboard.width = m.width
		m.dashboard.height = m.height
		m.screen = screenDashboard
		return m, m.dashboard.Init()
	case menuActionUp, menuActionDown:
		return m.startCompose(a)
	}
	return m, nil
}

// startCompose builds and dispatches the docker compose subprocess
// for menuActionUp / menuActionDown. Both use the standard pattern
// (suspend TUI, run, return to a summary screen).
func (m *Model) startCompose(a menuAction) (tea.Model, tea.Cmd) {
	cmd, title, label := buildComposeCommand(m.outDir, a)
	m.action = actionModel{title: title, command: label}
	m.screen = screenAction
	return m, runAction(cmd)
}

// updateDashboard forwards the message to the dashboard's own
// Update and afterwards checks if the dashboard wants to exit
// (q with no pending changes, or after the confirm-and-save flow).
// When yes, switch back to the menu.
func (m *Model) updateDashboard(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.dashboard == nil {
		// Lazy-load failed; only Esc / Enter / q gets the user back
		// to the menu so they can run Generate first.
		k, ok := msg.(tea.KeyMsg)
		if !ok {
			return m, nil
		}
		switch k.String() {
		case "esc", "enter", "q":
			m.screen = screenMenu
			m.dashboardErr = nil
		}
		return m, nil
	}
	updated, cmd := m.dashboard.Update(msg)
	if dm, ok := updated.(*dashboardModel); ok {
		m.dashboard = dm
	}
	if m.dashboard.requestExit {
		m.dashboard.requestExit = false
		m.screen = screenMenu
		// Drop the dashboard so a re-entry reloads from disk.
		// Cheap, and ensures changes saved here show fresh state on
		// next visit.
		m.dashboard = nil
	}
	return m, cmd
}

// updateGenerate forwards to the wizard, then checks for exit /
// dispatch signals.
func (m *Model) updateGenerate(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	updated, cmd := m.generate.Update(k)
	m.generate = updated
	if m.generate.requestExit {
		m.generate.requestExit = false
		m.screen = screenMenu
		return m, cmd
	}
	if m.generate.dispatchAction != nil {
		m.action = actionModel{
			title:   m.generate.dispatchTitle,
			command: m.generate.dispatchCommand,
		}
		exec := m.generate.dispatchAction
		m.generate.dispatchAction = nil
		m.screen = screenAction
		return m, runAction(exec)
	}
	return m, cmd
}

// updateAction handles the post-run summary screen. The actionDoneMsg
// arrives via tea.ExecProcess when the subprocess exits; key input
// after that returns the user to the menu.
func (m *Model) updateAction(msg tea.Msg) (tea.Model, tea.Cmd) {
	if done, ok := msg.(actionDoneMsg); ok {
		m.action.done = true
		m.action.err = done.err
		return m, nil
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		updated, cmd, exit := m.action.Update(k)
		m.action = updated
		if exit {
			m.screen = screenMenu
			return m, cmd
		}
		return m, cmd
	}
	return m, nil
}

// View dispatches rendering to the active sub-screen.
func (m *Model) View() string {
	switch m.screen {
	case screenMenu:
		return m.menu.View(m.outDir, m.width)
	case screenDashboard:
		if m.dashboard == nil {
			return renderDashboardError(m.outDir, m.dashboardErr, m.width)
		}
		return m.dashboard.View()
	case screenGenerate:
		return m.generate.View(m.width)
	case screenAction:
		return m.action.View(m.outDir, m.width)
	}
	return ""
}
