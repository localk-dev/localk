package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/localk-dev/localk/internal/compose"
	"github.com/localk-dev/localk/internal/devmode"
)

const overlayFilename = "docker-compose.dev.yml"

// runDev dispatches `localk dev` based on which mode flag the user
// passed. Single positional arg + --port: enter dev mode for a service.
// --stop: leave dev mode. --list: print what's currently in dev mode.
func runDev(args []string) {
	fs := flag.NewFlagSet("dev", flag.ExitOnError)
	outDir := fs.String("out-dir", ".", "directory containing the generated docker-compose.yml")
	hostPort := fs.Int("port", 0, "host port your local process will listen on (required when entering dev mode)")
	containerPort := fs.Int("container-port", 0, "override the in-network port (defaults to the service's first published container port, or 80 if none)")
	stop := fs.String("stop", "", "leave dev mode for the named service")
	list := fs.Bool("list", false, "list services currently in dev mode")

	args = reorderFlagsFirst(args)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	overlayPath := filepath.Join(*outDir, overlayFilename)

	switch {
	case *list:
		runDevList(overlayPath)
	case *stop != "":
		runDevStop(*stop, *outDir, overlayPath)
	default:
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "dev: missing <service> argument")
			fmt.Fprintln(os.Stderr, "usage:")
			fmt.Fprintln(os.Stderr, "  localk dev <service> --port <host-port> [--out-dir <dir>]")
			fmt.Fprintln(os.Stderr, "  localk dev --stop <service> [--out-dir <dir>]")
			fmt.Fprintln(os.Stderr, "  localk dev --list [--out-dir <dir>]")
			os.Exit(2)
		}
		if *hostPort <= 0 {
			fail("dev: --port is required when entering dev mode (e.g. --port 3000)")
		}
		runDevStart(fs.Arg(0), *hostPort, *containerPort, *outDir, overlayPath)
	}
}

// runDevStart adds (or replaces) the proxy entry for service in the
// overlay file, then prints connection guidance for the developer:
// where to bind their local process, how to reach other services,
// and rewritten env vars they can paste into their IDE config.
func runDevStart(service string, hostPort, explicitContainerPort int, outDir, overlayPath string) {
	base, err := loadBaseCompose(outDir)
	if err != nil {
		fail("%v", err)
	}
	if _, ok := base.Services[service]; !ok {
		known := serviceNames(base)
		fail("dev: %q is not a service in %s\n  known services: %s", service, filepath.Join(outDir, "docker-compose.yml"), joinHints(known))
	}

	containerPort := explicitContainerPort
	if containerPort == 0 {
		containerPort = devmode.ContainerPortFor(base.Services[service].Ports, 80)
	}

	overlay, _, err := devmode.Load(overlayPath)
	if err != nil {
		fail("%v", err)
	}
	overlay.AddProxy(service, containerPort, hostPort)
	if err := overlay.Save(overlayPath); err != nil {
		fail("%v", err)
	}

	printDevStartGuide(service, hostPort, containerPort, outDir, base)
}

// runDevStop removes the entry for service from the overlay (if
// present) and saves. We don't auto-restart the original service in
// compose — the next `localk up` (or a manual `docker compose up -d
// <service>`) will pick it up cleanly. Restarting from in here would
// require coupling dev mode to docker compose's lifecycle in a way
// that's surprising on its own.
func runDevStop(service, outDir, overlayPath string) {
	overlay, _, err := devmode.Load(overlayPath)
	if err != nil {
		fail("%v", err)
	}
	if !overlay.RemoveProxy(service) {
		fail("dev --stop: %q wasn't in dev mode (overlay: %s)", service, overlayPath)
	}
	if err := overlay.Save(overlayPath); err != nil {
		fail("%v", err)
	}
	fmt.Printf("Service %q is no longer in dev mode.\n", service)
	if len(overlay.Services) == 0 {
		fmt.Printf("Removed empty %s.\n", overlayPath)
	}
	fmt.Println()
	fmt.Printf("Run `localk up --out-dir %s` to bounce the original service back into the stack.\n", outDir)
}

// runDevList prints the services currently in dev mode by parsing the
// overlay file. Handles the "no file" case by printing an explicit
// "nothing in dev mode" message rather than an empty success.
func runDevList(overlayPath string) {
	overlay, exists, err := devmode.Load(overlayPath)
	if err != nil {
		fail("%v", err)
	}
	if !exists || len(overlay.Services) == 0 {
		fmt.Println("Nothing currently in dev mode.")
		return
	}
	names := overlay.ProxyNames()
	fmt.Printf("Services in dev mode (overlay: %s):\n", overlayPath)
	for _, n := range names {
		entry := overlay.Services[n]
		// The forward-target host port is the last token of the
		// "TCP:host.docker.internal:<port>" entrypoint arg. We re-derive
		// it for display rather than threading state through; keeps the
		// overlay file self-describing.
		var dest string
		if len(entry.Entrypoint) > 0 {
			dest = entry.Entrypoint[len(entry.Entrypoint)-1]
		}
		fmt.Printf("  %s  →  %s\n", n, dest)
	}
}

// loadBaseCompose reads the docker-compose.yml that `localk generate`
// produced. Thin wrapper around compose.LoadFile that adds the
// command-specific "run `localk generate` first" hint when the file
// is missing — keeps the friendly UX without duplicating the parser.
func loadBaseCompose(outDir string) (*compose.File, error) {
	path := filepath.Join(outDir, "docker-compose.yml")
	f, err := compose.LoadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			abs, _ := filepath.Abs(path)
			return nil, fmt.Errorf("dev: compose file not found at %s\n  run `localk generate <input>` first, or pass --out-dir to point at an existing one", abs)
		}
		return nil, err
	}
	return f, nil
}

func serviceNames(f *compose.File) []string {
	names := make([]string, 0, len(f.Services))
	for name := range f.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func joinHints(names []string) string {
	if len(names) <= 8 {
		return joinComma(names)
	}
	// Long list: show the first few alphabetically, then a count.
	return joinComma(names[:8]) + fmt.Sprintf(" (and %d more)", len(names)-8)
}

func joinComma(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// printDevStartGuide is the user-facing output after a successful
// `localk dev <service>`. The goal is that the dev can paste the
// printed env vars into their IDE and immediately have a runnable
// process — no separate translation step.
func printDevStartGuide(service string, hostPort, containerPort int, outDir string, base *compose.File) {
	fmt.Printf("Service %q is now in dev mode.\n\n", service)
	fmt.Printf("Run your code on:        localhost:%d\n", hostPort)
	fmt.Printf("Reachable from stack at: http://%s:%d\n\n", service, containerPort)

	// Reference table: which services have published host ports your
	// local process can reach as `localhost:<port>`.
	portsByService := map[string][]string{}
	for name, s := range base.Services {
		if name == service {
			continue // the service in dev mode is no longer published
		}
		if len(s.Ports) > 0 {
			portsByService[name] = s.Ports
		}
	}
	hostPorts := devmode.HostPortsFromBase(portsByService)
	if len(hostPorts) > 0 {
		fmt.Println("Other services reachable from your laptop:")
		for _, hp := range hostPorts {
			fmt.Printf("  %-30s localhost:%d\n", hp.Service, hp.Port)
		}
		fmt.Println()
	}

	// Build the env-var pile the dev needs in their IDE: the service's
	// own `environment:` entries from compose (typical home of
	// connection strings like DATABASE_URL) + the .env file (typical
	// home of secrets). Both run through the same hostname rewrite, so
	// `postgres://api:5432` becomes `postgres://localhost:5432` for
	// services with published host ports.
	type envEntry struct{ key, val string }
	var entries []envEntry

	if me := base.Services[service]; me.Environment != nil {
		// Sort keys so output is stable across runs.
		keys := make([]string, 0, len(me.Environment))
		for k := range me.Environment {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			entries = append(entries, envEntry{k, me.Environment[k]})
		}
	}

	envPath := filepath.Join(outDir, ".env")
	envBytes, _ := os.ReadFile(envPath)
	envText := string(envBytes)

	if len(entries) > 0 || envText != "" {
		fmt.Println("Useful env vars (hostnames remapped for host access — review before pasting):")
		// First the structured environment entries — rewrite each value.
		for _, e := range entries {
			rewrittenVal := devmode.RewriteEnvForHost(e.key+"="+e.val, portsByService)
			// RewriteEnvForHost preserves the KEY=VALUE shape; strip the
			// "key=" prefix back off so we control the indentation.
			val := rewrittenVal[len(e.key)+1:]
			fmt.Printf("  %s=%s\n", e.key, val)
		}
		// Then the .env contents (skipping comments + blanks for readability).
		if envText != "" {
			rewritten := devmode.RewriteEnvForHost(envText, portsByService)
			for _, line := range splitNonEmptyLines(rewritten) {
				fmt.Printf("  %s\n", line)
			}
		}
		fmt.Println()
	}

	fmt.Printf("When done: localk dev --stop %s --out-dir %s\n", service, outDir)
}

// splitNonEmptyLines drops blank lines and comments from .env-style
// content for nicer printout. The raw rewritten content is fine to
// write to a file but visually noisy in a terminal banner.
func splitNonEmptyLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			line := s[start:i]
			start = i + 1
			trim := line
			for len(trim) > 0 && (trim[0] == ' ' || trim[0] == '\t') {
				trim = trim[1:]
			}
			if trim == "" || trim[0] == '#' {
				continue
			}
			out = append(out, line)
		}
	}
	return out
}
