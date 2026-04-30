package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// pollInterval is how often the TUI re-runs `docker compose ps` to
// refresh the per-service runtime state. Two seconds feels live
// without making the docker daemon work too hard for a 60-service
// stack.
const pollInterval = 2 * time.Second

// runtimeMsg carries the result of one `docker compose ps` poll
// back into the Update loop. Either a populated map of
// service-name -> state, or an error to display in the status banner.
type runtimeMsg struct {
	state map[string]string
	err   error
}

// tickMsg is the timer event that schedules the next poll. We send
// it via tea.Tick so the polling cadence stays inside the
// Bubble Tea event loop and we don't need a separate goroutine that
// outlives the program.
type tickMsg struct{}

// pollCmd kicks off one `docker compose ps --format json` and
// returns its result as a runtimeMsg. The compose file argument is
// passed via -f so the poller works from any cwd.
//
// We also include the dev and disable overlays if they exist so the
// service set matches what `localk up` would actually be running —
// a service tagged with profiles: ["disabled"] still shows in `ps`
// but as a placeholder; including the overlay makes the row state
// match reality.
func pollCmd(composePath, devPath, disablePath string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		args := []string{"compose", "-f", composePath}
		if devPath != "" && fileExists(devPath) {
			args = append(args, "-f", devPath)
		}
		if disablePath != "" && fileExists(disablePath) {
			args = append(args, "-f", disablePath)
		}
		args = append(args, "ps", "--format", "json")

		out, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			// Surface a short, actionable message instead of the raw
			// exec error. The user almost certainly hit one of: docker
			// not installed, docker daemon down, stack not yet `up`d.
			msg := condenseExecError(err)
			return runtimeMsg{err: errFromString(msg)}
		}

		state, perr := parsePsOutput(out)
		if perr != nil {
			return runtimeMsg{err: perr}
		}
		return runtimeMsg{state: state}
	}
}

// scheduleTick returns a Cmd that fires a tickMsg after pollInterval.
// The Update loop chains pollCmd off each tick so we never busy-loop.
func scheduleTick() tea.Cmd {
	return tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// parsePsOutput handles both shapes that `docker compose ps --format
// json` produces depending on compose version: a single JSON array,
// or one JSON object per line (newline-delimited). Returns a
// service-name -> state map.
func parsePsOutput(out []byte) (map[string]string, error) {
	type entry struct {
		Service string `json:"Service"`
		State   string `json:"State"`
	}
	state := map[string]string{}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return state, nil
	}

	// Try array form first.
	if trimmed[0] == '[' {
		var items []entry
		if err := json.Unmarshal([]byte(trimmed), &items); err == nil {
			for _, e := range items {
				if e.Service != "" {
					state[e.Service] = e.State
				}
			}
			return state, nil
		}
		// Fall through to NDJSON if the array parse fails — some
		// versions emit `[\n{...}\n{...}\n]` which valid JSON
		// libraries handle but defensive in case of weirdness.
	}

	// NDJSON: one entry per line.
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "[" || line == "]" {
			continue
		}
		// Strip trailing comma if compose ever emits "pretty" array form.
		line = strings.TrimSuffix(line, ",")
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Service != "" {
			state[e.Service] = e.State
		}
	}
	return state, nil
}

// condenseExecError shortens common docker compose error shapes to
// one line suitable for the TUI's status banner. The full output
// would line-wrap and clobber the layout.
func condenseExecError(err error) string {
	msg := err.Error()
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		first := strings.SplitN(strings.TrimSpace(string(ee.Stderr)), "\n", 2)[0]
		if first != "" {
			return first
		}
	}
	return strings.SplitN(msg, "\n", 2)[0]
}

// errFromString wraps a string into the error interface without
// pulling in errors.New — keeps imports minimal, and the resulting
// error is only ever shown via Error().
type stringErr string

func (s stringErr) Error() string  { return string(s) }
func errFromString(s string) error { return stringErr(s) }

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
