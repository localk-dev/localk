package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// buildComposeCommand assembles the docker compose subprocess for
// menuActionUp / menuActionDown. Returns the exec.Cmd, a short
// title for the post-run screen, and a human-readable label of the
// command line.
//
// The compose file path is the same one localk's CLI looks for —
// `<outDir>/docker-compose.yml` — and the same dev/disable overlay
// detection happens server-side via `-f` flags. We don't try to
// be cleverer here; this is a thin convenience wrapper.
func buildComposeCommand(outDir string, a menuAction) (*exec.Cmd, string, string) {
	composePath := filepath.Join(outDir, composeFilename)
	args := []string{"compose", "-f", composePath}
	args = appendOverlayIfPresent(args, outDir, devFilename)
	args = appendOverlayIfPresent(args, outDir, disableFilename)

	var title string
	switch a {
	case menuActionUp:
		args = append(args, "up", "-d")
		title = "Up"
	case menuActionDown:
		args = append(args, "down")
		title = "Down"
	}
	cmd := exec.Command("docker", args...)
	label := "docker " + strings.Join(args, " ")
	return cmd, title, label
}

// appendOverlayIfPresent extends args with `-f <overlay>` when the
// overlay file exists alongside the base compose file. Mirrors what
// `localk up` / `localk down` do — keeps the TUI's compose calls
// using exactly the same overlay set the CLI would pick up.
func appendOverlayIfPresent(args []string, outDir, name string) []string {
	path := filepath.Join(outDir, name)
	if _, err := os.Stat(path); err == nil {
		return append(args, "-f", path)
	}
	return args
}

// renderDashboardError renders the friendly inline error shown in
// the dashboard subscreen when newDashboardModel failed to load —
// typically because no docker-compose.yml exists yet. Tells the
// user how to fix it instead of just dumping the raw error.
func renderDashboardError(outDir string, err error, width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("localk") + subHeaderStyle.Render(" — dashboard"))
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", maxWidth(width, 80))))
	b.WriteString("\n\n")
	if err == nil {
		b.WriteString(errorStyle.Render("dashboard not loaded"))
	} else {
		b.WriteString(errorStyle.Render("✗ " + err.Error()))
		b.WriteString("\n\n")
		composePath := filepath.Join(outDir, composeFilename)
		b.WriteString(subHeaderStyle.Render("expected compose file at: " + composePath))
		b.WriteString("\n")
		b.WriteString(subHeaderStyle.Render("run Generate from the menu first to create it."))
	}
	b.WriteString("\n\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", maxWidth(width, 80))))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render("enter / esc / q  return to menu"))
	return b.String()
}
