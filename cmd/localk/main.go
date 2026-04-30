package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/localk-dev/localk/internal/config"
	"github.com/localk-dev/localk/internal/convert"
	"github.com/localk-dev/localk/internal/kube"
	"github.com/localk-dev/localk/internal/kubectl"
)

const usage = `localk - run your Kubernetes stack locally with one command.

Usage:
  localk generate <input-dir> [--out-dir <dir>] [-o <file>] [--config <file>] [--dry-run]
  localk generate -k [-n <namespace>] [--context <name>] [-y] [--out-dir <dir>] [--config <file>] [--dry-run]
  localk up   [--out-dir <dir>] [-f <file>] [--build] [--no-detach] [--disable <list>] [-- DOCKER_COMPOSE_ARGS...]
  localk down [--out-dir <dir>] [-f <file>] [-v] [-- DOCKER_COMPOSE_ARGS...]
  localk dev  <service> --port <host-port> [--out-dir <dir>] [--container-port <n>]
  localk dev  --stop <service> [--out-dir <dir>]
  localk dev  --list [--out-dir <dir>]
  localk disable <service> [<service> ...]   [--out-dir <dir>]
  localk disable --restore <service>         [--out-dir <dir>]
  localk disable --clear                     [--out-dir <dir>]
  localk disable --list                      [--out-dir <dir>]
  localk tui                                 [--out-dir <dir>]
  localk version
  localk help

Per-service overrides (skip, image, build) live in localk.yaml. localk
looks for ./localk.yaml by default; pass --config to use a different path.

Commands:
  generate    Convert k8s manifests into a docker-compose.yml.
              Source: a directory of YAML files, or a live cluster via -k.
  up          Run the generated stack via 'docker compose up' (detached
              by default). Looks for ./docker-compose.yml unless --out-dir
              or -f is given.
  down        Stop the stack via 'docker compose down'. Pass -v to also
              delete named volumes (DESTRUCTIVE).
  dev         Put one service into dev mode: replace it in compose with a
              proxy that forwards traffic to host.docker.internal:<port>,
              so you can run that service in your IDE while the rest of
              the stack keeps running. --stop restores. --list shows what's
              currently in dev mode.
  disable     Stop named services from starting on 'localk up'. Sticky:
              persists across runs in docker-compose.disable.yml.
              --restore <service> brings one back; --clear empties the
              list; --list shows what's disabled.
              'localk up --disable foo,bar' adds transient disables on
              top of the sticky list (one-shot, no file change).
  tui         Interactive dashboard: scroll the service list, toggle
              disable / dev mode with single keystrokes, save and exit.
              Reads/writes the same overlays as 'disable' and 'dev'.
  version     Print version and exit.
  help        Print this help and exit.

Cluster mode (-k):
  Pulls Deployments, Services, ConfigMaps, Secrets, and PVCs from the
  current kubeconfig context and namespace. Read-only — localk only ever
  invokes 'kubectl get' and 'kubectl config view/current-context'.

Dry run (--dry-run):
  Print what would be written instead of saving to disk. Compose YAML is
  shown in full; .env values are redacted so secrets don't end up in your
  scrollback. Useful before running '-k' against a real cluster.
`

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "generate":
		runGenerate(os.Args[2:])
	case "up":
		runUp(os.Args[2:])
	case "down":
		runDown(os.Args[2:])
	case "dev":
		runDev(os.Args[2:])
	case "disable":
		runDisable(os.Args[2:])
	case "tui":
		runTui(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("localk", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func runGenerate(args []string) {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	output := fs.String("o", "docker-compose.yml", "output file path (relative paths are resolved against --out-dir)")
	envOutput := fs.String("env-out", ".env", "output file for secret-derived env vars (relative paths are resolved against --out-dir)")
	outDir := fs.String("out-dir", ".", "directory to write outputs into; created if missing")
	fromCluster := fs.Bool("k", false, "pull manifests from the current kubectl context (read-only)")
	fs.BoolVar(fromCluster, "from-cluster", false, "pull manifests from the current kubectl context (read-only)")
	namespace := fs.String("n", "", "kubectl namespace (defaults to current kubeconfig namespace)")
	fs.StringVar(namespace, "namespace", "", "kubectl namespace (defaults to current kubeconfig namespace)")
	kubeContext := fs.String("context", "", "kubectl context (defaults to current kubeconfig context)")
	yes := fs.Bool("y", false, "skip the confirmation prompt in cluster mode")
	fs.BoolVar(yes, "yes", false, "skip the confirmation prompt in cluster mode")
	dryRun := fs.Bool("dry-run", false, "print what would be written without touching disk (.env values redacted)")
	configPath := fs.String("config", "localk.yaml", "path to localk.yaml override file (silently ignored if missing)")
	memoryMode := fs.String("memory", "auto", "memory limit policy: auto (scale to fit Docker memory), preserve (declared k8s values), drop (no limits)")
	cpuMode := fs.String("cpu", "auto", "CPU limit policy: auto (scale to fit Docker CPU count), preserve (declared k8s values), drop (no limits)")
	platformMode := fs.String("platform", "auto", "platform pinning policy: auto (set linux/amd64 on arm64 hosts), native (no pinning), or a literal value like linux/amd64")

	args = reorderFlagsFirst(args)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	composePath := resolveOutPath(*outDir, *output)
	envPath := resolveOutPath(*outDir, *envOutput)

	var (
		manifests *kube.Manifests
		err       error
		envHeader string
	)

	if *fromCluster {
		if fs.NArg() > 0 {
			fail("generate: cannot pass an input directory together with -k")
		}
		manifests, err = loadFromCluster(*namespace, *kubeContext, *yes)
		if err != nil {
			fail("%v", err)
		}
		envHeader = "# Generated by localk from a live Kubernetes cluster.\n" +
			"# These are real cluster Secret values. Do not commit this file.\n"
	} else {
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "generate: missing <input-dir> argument (or pass -k to pull from a cluster)")
			fmt.Fprintln(os.Stderr, "usage: localk generate <input-dir> [-o <output-file>]")
			fmt.Fprintln(os.Stderr, "       localk generate -k [-n <namespace>] [-o <output-file>]")
			os.Exit(2)
		}
		inputDir := fs.Arg(0)
		manifests, err = kube.ParseDir(inputDir)
		if err != nil {
			fail("parsing %s: %v", inputDir, err)
		}
		if !hasWorkloads(manifests) {
			fail("no Deployments or StatefulSets found under %s. localk needs at least one workload to generate a compose file.", inputDir)
		}
	}

	if *fromCluster && !hasWorkloads(manifests) {
		fail("no Deployments or StatefulSets found in namespace %q (context %q). Pick a namespace that contains at least one workload.",
			displayNamespace(*namespace), displayContext(*kubeContext))
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fail("%v", err)
	}

	result, err := convert.Convert(manifests, cfg)
	if err != nil {
		fail("converting manifests: %v", err)
	}

	// Resource rebalance pass. By default this auto-scales memory/CPU
	// limits to fit Docker's reported capacity (with a safety margin)
	// so users with prod-sized limits in their manifests don't end
	// up with a compose file that asks for 100+ GB of RAM. The
	// --memory / --cpu flags override.
	convert.RebalanceResources(result, convert.ResourceMode(*memoryMode), convert.ResourceMode(*cpuMode), convert.DockerProbe{})

	// Platform pinning. Default `auto` sets linux/amd64 on arm64
	// hosts (M-series Macs) so amd64-only private registries don't
	// produce "no matching manifest" pull errors. amd64 hosts: no-op.
	convert.ApplyPlatform(result, convert.PlatformMode(*platformMode), runtime.GOARCH)

	composeBytes, err := yaml.Marshal(result.Compose)
	if err != nil {
		fail("marshaling compose file: %v", err)
	}

	header := []byte(
		"# Generated by localk. Do not edit by hand — regenerate from your k8s manifests instead.\n" +
			"# See https://localk.dev for documentation.\n\n",
	)
	composeOut := append(header, composeBytes...)

	envContents := ""
	if result.EnvFile != "" {
		envContents = result.EnvFile
		if envHeader != "" {
			envContents = envHeader + envContents
		}
	}

	caddyPath := resolveOutPath(*outDir, "Caddyfile")

	if *dryRun {
		printDryRun(composePath, composeOut, envPath, envContents, caddyPath, result.CaddyFile, len(result.Compose.Services))
		for _, w := range result.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		fmt.Println()
		fmt.Println("Dry run only — no files written. Re-run without --dry-run to save.")
		return
	}

	if err := ensureParentDir(composePath); err != nil {
		fail("preparing output directory for %s: %v", composePath, err)
	}
	if err := writeFile(composePath, composeOut); err != nil {
		fail("writing %s: %v", composePath, err)
	}
	if envContents != "" {
		if err := ensureParentDir(envPath); err != nil {
			fail("preparing output directory for %s: %v", envPath, err)
		}
		if err := writeFile(envPath, []byte(envContents)); err != nil {
			fail("writing %s: %v", envPath, err)
		}
	}
	if result.CaddyFile != "" {
		if err := ensureParentDir(caddyPath); err != nil {
			fail("preparing output directory for %s: %v", caddyPath, err)
		}
		if err := writeFile(caddyPath, []byte(result.CaddyFile)); err != nil {
			fail("writing %s: %v", caddyPath, err)
		}
	}

	// ConfigMap- and Secret-backed volume mounts: write each entry's
	// data to a file under <out-dir>/configs/<name>/ (or secrets/).
	// Compose then bind-mounts the whole directory into the
	// container, giving the app the same per-key filenames it'd see
	// in k8s.
	//
	// File modes: configs/ get 0755 (executable) — Bitnami helm
	// charts mount their setup scripts via subPath and `exec` them,
	// so without +x we'd see "exec ...: permission denied". Secrets
	// stay at 0600 since they're sensitive cluster data and code
	// reads them, never execs them.
	for relPath, content := range result.ConfigFiles {
		fullPath := filepath.Join(*outDir, relPath)
		if err := ensureParentDir(fullPath); err != nil {
			fail("preparing directory for %s: %v", fullPath, err)
		}
		mode := materializedFileMode(relPath)
		if err := writeFileMode(fullPath, []byte(content), mode); err != nil {
			fail("writing %s: %v", fullPath, err)
		}
	}

	abs, _ := filepath.Abs(composePath)
	fmt.Printf("Wrote %s (%d services)\n", abs, len(result.Compose.Services))
	if envContents != "" {
		envAbs, _ := filepath.Abs(envPath)
		fmt.Printf("Wrote %s with secret-derived env vars. Review before sharing.\n", envAbs)
	}
	if result.CaddyFile != "" {
		caddyAbs, _ := filepath.Abs(caddyPath)
		fmt.Printf("Wrote %s for the Caddy reverse proxy.\n", caddyAbs)
	}
	if len(result.ConfigFiles) > 0 {
		fmt.Printf("Wrote %d config/secret file(s) under %s/{configs,secrets}/.\n",
			len(result.ConfigFiles), *outDir)
	}

	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}

	fmt.Println()
	fmt.Println("Next: docker compose -f", composePath, "up")
}

// resolveOutPath joins out path against out-dir when out path is relative,
// and returns out path unchanged when it's absolute. Lets users pick a
// single output directory while still allowing per-file overrides.
func resolveOutPath(dir, file string) string {
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(dir, file)
}

// ensureParentDir creates the directory containing path if it doesn't exist.
// We tolerate the empty / "." case so the default ("./docker-compose.yml")
// doesn't fail on systems where the cwd is unusual.
func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

// printDryRun streams the compose file, a redacted version of the .env
// file, and the Caddyfile to stdout, prefixed by "would write" markers, so
// the user can see the full output without secrets ending up in their
// terminal scrollback.
func printDryRun(composePath string, composeOut []byte, envPath, envContents, caddyPath, caddyContents string, serviceCount int) {
	composeAbs, _ := filepath.Abs(composePath)
	fmt.Printf("--- DRY RUN: would write %s (%d services) ---\n", composeAbs, serviceCount)
	os.Stdout.Write(composeOut)
	if !strings.HasSuffix(string(composeOut), "\n") {
		fmt.Println()
	}

	if envContents != "" {
		envAbs, _ := filepath.Abs(envPath)
		fmt.Println()
		fmt.Printf("--- DRY RUN: would write %s (values redacted) ---\n", envAbs)
		fmt.Println(redactEnvFile(envContents))
	}

	if caddyContents != "" {
		caddyAbs, _ := filepath.Abs(caddyPath)
		fmt.Println()
		fmt.Printf("--- DRY RUN: would write %s ---\n", caddyAbs)
		fmt.Print(caddyContents)
		if !strings.HasSuffix(caddyContents, "\n") {
			fmt.Println()
		}
	}
}

// redactEnvFile replaces every `KEY=value` pair with `KEY=<redacted>`,
// preserving comments and blank lines. The compose file references these
// keys via env_file, so seeing the key names is enough to verify shape;
// values are never useful to display in a preview.
func redactEnvFile(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		if i := strings.Index(line, "="); i > 0 {
			out = append(out, line[:i]+"=<redacted>")
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// loadFromCluster pulls manifests via kubectl and returns them parsed.
// It resolves the active context+namespace, prints them, and (unless -y is
// set) requires the user to confirm before any kubectl get is issued.
func loadFromCluster(nsFlag, ctxFlag string, skipPrompt bool) (*kube.Manifests, error) {
	runner := kubectl.Default()
	if err := kubectl.Available(runner); err != nil {
		return nil, err
	}

	resolvedCtx := ctxFlag
	if resolvedCtx == "" {
		c, err := kubectl.CurrentContext(runner)
		if err != nil {
			return nil, fmt.Errorf("resolving current context: %w", err)
		}
		resolvedCtx = c
	}

	resolvedNs := nsFlag
	if resolvedNs == "" {
		n, err := kubectl.CurrentNamespace(runner)
		if err != nil {
			return nil, fmt.Errorf("resolving current namespace: %w", err)
		}
		resolvedNs = n
	}

	if !skipPrompt {
		if !stdinIsTerminal() {
			return nil, fmt.Errorf("non-interactive mode requires --yes to skip the confirmation prompt")
		}
		fmt.Printf("Cluster context: %s\n", resolvedCtx)
		fmt.Printf("Namespace:       %s\n", resolvedNs)
		fmt.Println("Pulling (read-only): Deployments, Services, ConfigMaps, Secrets, PVCs")
		fmt.Println("localk only invokes `kubectl get` and `kubectl config view`.")
		fmt.Println("It never modifies, creates, or deletes anything in the cluster.")
		fmt.Print("Continue? [y/N] ")
		if !readYes(os.Stdin) {
			return nil, fmt.Errorf("aborted by user")
		}
	}

	out, err := kubectl.Fetch(runner, context.Background(), kubectl.FetchOptions{
		Namespace: resolvedNs,
		Context:   ctxFlag, // pass through only when explicitly set
	})
	if err != nil {
		return nil, fmt.Errorf("kubectl fetch: %w", err)
	}

	m, err := kube.ParseBytes(out)
	if err != nil {
		return nil, fmt.Errorf("parsing kubectl output: %w", err)
	}
	return m, nil
}

func readYes(r *os.File) bool {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}

// stdinIsTerminal reports whether os.Stdin is connected to a terminal. We
// check this without taking a new dependency by looking at the file mode —
// character devices are TTYs, regular files / pipes are not.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// hasWorkloads reports whether the parsed input has at least one
// Deployment or StatefulSet to convert. Without one of these, there's
// nothing to put in the compose file.
func hasWorkloads(m *kube.Manifests) bool {
	return len(m.Deployments) > 0 || len(m.StatefulSets) > 0
}

func displayNamespace(n string) string {
	if n == "" {
		return "(current)"
	}
	return n
}

func displayContext(c string) string {
	if c == "" {
		return "(current)"
	}
	return c
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

func writeFileMode(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}

// materializedFileMode picks the on-disk mode for a config/secret
// file. ConfigMap-derived files default to 0755 because helm charts
// (Bitnami especially) commonly mount a single key as an executable
// script via subPath; without +x the container fails with "exec ...:
// permission denied". Secret files use 0644 to match k8s' default
// projected-secret mode — containers commonly run as a non-root
// user (rabbitmq is UID 1001) and the host UID writing these files
// is different, so 0600 leaves the in-container reader unable to
// open them. The data is already on the developer's disk under
// secrets/; the marginal protection of 0600 isn't worth breaking
// every Bitnami chart that reads its own secret files.
func materializedFileMode(relPath string) os.FileMode {
	if strings.HasPrefix(relPath, "secrets/") {
		return 0o644
	}
	return 0o755
}

// reorderFlagsFirst pulls every -flag (and its argument when separate) to the
// front of the slice, leaving positional arguments at the back. This lets us
// accept `generate ./k8s -o out.yml` even though stdlib's flag package would
// otherwise stop parsing at `./k8s`.
func reorderFlagsFirst(args []string) []string {
	knownFlags := map[string]bool{
		"-o":               true,
		"-env-out":         true,
		"--env-out":        true,
		"--o":              true,
		"-n":               true,
		"--namespace":      true,
		"--context":        true,
		"-out-dir":         true,
		"--out-dir":        true,
		"-config":          true,
		"--config":         true,
		"-memory":          true,
		"--memory":         true,
		"-cpu":             true,
		"--cpu":            true,
		"-platform":        true,
		"--platform":       true,
		"-port":            true,
		"--port":           true,
		"-container-port":  true,
		"--container-port": true,
		"-stop":            true,
		"--stop":           true,
		"-restore":         true,
		"--restore":        true,
		"-disable":         true,
		"--disable":        true,
	}
	knownBoolFlags := map[string]bool{
		"-k":             true,
		"--from-cluster": true,
		"-y":             true,
		"--yes":          true,
		"--dry-run":      true,
		"-list":          true,
		"--list":         true,
		"-clear":         true,
		"--clear":        true,
	}

	var flagsArgs, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if eq := indexEq(a); eq > 0 && (knownFlags[a[:eq]] || knownBoolFlags[a[:eq]]) {
			flagsArgs = append(flagsArgs, a)
			continue
		}
		if knownBoolFlags[a] {
			flagsArgs = append(flagsArgs, a)
			continue
		}
		if knownFlags[a] {
			flagsArgs = append(flagsArgs, a)
			if i+1 < len(args) {
				flagsArgs = append(flagsArgs, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flagsArgs, positional...)
}

func indexEq(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return i
		}
	}
	return -1
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "localk: "+format+"\n", args...)
	os.Exit(1)
}
