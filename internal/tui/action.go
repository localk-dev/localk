package tui

import (
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// actionModel is the shared post-run screen for any subprocess
// localk launches from the TUI: docker compose up/down, and the
// final step of the generate wizard. The work itself happens via
// tea.ExecProcess (which suspends the TUI, runs the subprocess with
// stdio inherited, then resumes); this model tracks what just
// happened and shows a one-screen summary while the user reads it.
type actionModel struct {
	// title appears at the top of the post-run screen, e.g.
	// "Generate" or "Up". Pre-formatted by the caller.
	title string
	// command is the command that was run, displayed verbatim so the
	// user knows what they invoked. Kept as a single string for
	// rendering simplicity.
	command string
	// err is the result from tea.ExecProcess: nil on success, or the
	// process's exit error.
	err error
	// done reports whether the subprocess has finished. While false
	// the TUI is suspended (we're not actually running our View);
	// once true we render the summary and wait for Enter to return
	// to the menu.
	done bool
}

// actionDoneMsg is the message tea.ExecProcess delivers when the
// subprocess exits. Carried err is whatever exec.Cmd.Run returned —
// nil on a clean exit, otherwise an *exec.ExitError or similar.
type actionDoneMsg struct {
	err error
}

// runAction is the standard entry point for kicking off a subprocess
// from any other sub-screen. Returns a Cmd suitable for return from
// the caller's Update; the caller is responsible for switching
// screen to screenAction beforehand and storing title/command on
// the actionModel.
func runAction(cmd *exec.Cmd) tea.Cmd {
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return actionDoneMsg{err: err}
	})
}

// Update handles key input on the action screen. It only reacts to
// keys after the subprocess has finished — while the subprocess is
// running, the TUI is suspended and our Update isn't called for
// keystrokes anyway. Returns (model, cmd, done) where done=true
// signals the parent Model to switch back to the menu.
func (a actionModel) Update(msg tea.KeyMsg) (actionModel, tea.Cmd, bool) {
	if !a.done {
		// While running, ignore stray key events. (In practice none
		// arrive — ExecProcess suspends — but be defensive.)
		return a, nil, false
	}
	switch msg.String() {
	case "enter", "esc", "q":
		// Signal parent to return to menu.
		return a, nil, true
	}
	return a, nil, false
}

// View renders the post-run summary. While the subprocess is still
// running this isn't displayed (TUI is suspended); ExecProcess
// resumes the TUI before delivering actionDoneMsg, so by the time
// View runs we have something to show.
func (a actionModel) View(outDir string, width int) string {
	var b strings.Builder

	// Header line matches the menu/dashboard style.
	left := headerStyle.Render("localk") + subHeaderStyle.Render(" — "+a.title)
	b.WriteString(left)
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", maxWidth(width, 80))))
	b.WriteString("\n\n")

	if a.command != "" {
		b.WriteString(subHeaderStyle.Render("ran:"))
		b.WriteString("\n  ")
		b.WriteString(a.command)
		b.WriteString("\n\n")
	}

	if a.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("✗ exited with error: %v", a.err)))
	} else {
		b.WriteString(statusRunning.Render("✓ completed"))
	}

	b.WriteString("\n\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", maxWidth(width, 80))))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render("enter / esc / q  return to menu"))
	return b.String()
}
