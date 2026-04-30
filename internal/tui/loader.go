package tui

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/localk-dev/localk/internal/compose"
	"github.com/localk-dev/localk/internal/devmode"
)

// Filenames the TUI reads/writes. Hardcoded here mirroring the
// constants in cmd/localk because internal packages can't import cmd.
// If they ever drift, both the typed commands and the TUI break in
// sync — a regression that's easy to catch.
const (
	composeFilename = "docker-compose.yml"
	devFilename     = "docker-compose.dev.yml"
	disableFilename = "docker-compose.disable.yml"
)

// New builds an initial Model by loading the base compose file and
// both overlays from outDir. Returns an error only when the base
// compose file is missing or unparseable — a missing overlay just
// means "no services in dev / disabled mode," which is normal.
func New(outDir string) (*Model, error) {
	composePath := filepath.Join(outDir, composeFilename)
	devPath := filepath.Join(outDir, devFilename)
	disablePath := filepath.Join(outDir, disableFilename)

	base, err := compose.LoadFile(composePath)
	if err != nil {
		return nil, err
	}
	dev, _, err := devmode.Load(devPath)
	if err != nil {
		return nil, err
	}
	disabled, _, err := devmode.LoadDisabled(disablePath)
	if err != nil {
		return nil, err
	}

	rows := buildRows(base, dev, disabled)

	// textinput components for filter and port modes. Pre-built here
	// so Update doesn't have to lazily initialize them.
	filterInput := textinput.New()
	filterInput.Placeholder = "filter services..."
	filterInput.CharLimit = 64

	portInput := textinput.New()
	portInput.Placeholder = "host port (e.g. 3000)"
	portInput.CharLimit = 5
	portInput.Width = 12

	m := &Model{
		outDir:      outDir,
		composePath: composePath,
		devPath:     devPath,
		disablePath: disablePath,
		base:        base,
		rows:        rows,
		visible:     allIndices(len(rows)),
		runtime:     map[string]string{},
		mode:        modeNormal,
		filter:      filterInput,
		port:        portInput,
	}
	return m, nil
}

// buildRows merges the base compose file with both overlays into a
// stable, sorted list of ServiceRow. Pure function — easy to unit
// test, no I/O.
func buildRows(base *compose.File, dev *devmode.Overlay, disabled *devmode.DisabledOverlay) []ServiceRow {
	rows := make([]ServiceRow, 0, len(base.Services))
	for name, svc := range base.Services {
		row := ServiceRow{
			Name:      name,
			Image:     svc.Image,
			Disabled:  disabled.IsDisabled(name),
			lowerName: strings.ToLower(name),
		}
		if proxy, inDev := dev.Services[name]; inDev {
			row.DevPort = parseDevForwardPort(proxy.Entrypoint)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].lowerName < rows[j].lowerName })
	return rows
}

// parseDevForwardPort recovers the host-side port from a proxy
// service's entrypoint args. The dev overlay stores it as the last
// element: "TCP:host.docker.internal:<port>". Returns 0 on any
// parse failure, which the renderer interprets as "in dev mode but
// port unknown" rather than "not in dev mode" — the row's presence
// in the dev overlay is what flags it as in-dev.
func parseDevForwardPort(entrypoint []string) int {
	if len(entrypoint) == 0 {
		return 0
	}
	last := entrypoint[len(entrypoint)-1]
	// Expected: "TCP:host.docker.internal:3000"
	idx := strings.LastIndex(last, ":")
	if idx < 0 {
		return 0
	}
	port := 0
	for _, c := range last[idx+1:] {
		if c < '0' || c > '9' {
			return 0
		}
		port = port*10 + int(c-'0')
	}
	return port
}

func allIndices(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// applyFilter recomputes m.visible based on the current filter text.
// Empty filter → every row visible. Otherwise: case-insensitive
// substring match on the service name. Cursor stays at the same row
// when possible, otherwise clamps to a valid index.
func (m *Model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		m.visible = allIndices(len(m.rows))
	} else {
		out := m.visible[:0]
		for i, r := range m.rows {
			if strings.Contains(r.lowerName, q) {
				out = append(out, i)
			}
		}
		m.visible = out
	}
	if m.cursor >= len(m.visible) {
		// Builtin max is available since Go 1.21; we're on 1.24+.
		m.cursor = max(0, len(m.visible)-1)
	}
}
