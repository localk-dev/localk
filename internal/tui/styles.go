package tui

import "github.com/charmbracelet/lipgloss"

// Styles for the dashboard. Kept central so the look can be retuned
// without spelunking through view.go. All colors use lipgloss's
// adaptive scheme (different value for light vs. dark terminals)
// where it matters; status colors stay constant for readability.

var (
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4"))

	subHeaderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	dividerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	// Cursor row background. Kept subtle so the row content stays
	// readable on light and dark terminals.
	cursorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7D56F4")).
			Bold(true)

	// Status indicator colors.
	statusRunning  = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")) // green
	statusEnabled  = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E"))
	statusStopped  = lipgloss.NewStyle().Foreground(lipgloss.Color("#A1A1AA"))  // grey
	statusDisabled = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))  // red
	statusDev      = lipgloss.NewStyle().Foreground(lipgloss.Color("#3B82F6")). // blue
			Bold(true)

	dirtyMarkerStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F59E0B")). // amber
				Bold(true)

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	flashStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F59E0B")). // amber
			Bold(true)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EF4444")). // red
			Bold(true)

	helpBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(1, 2)

	modalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#7D56F4")).
			Padding(1, 2)
)
