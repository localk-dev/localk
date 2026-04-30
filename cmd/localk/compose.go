package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// runUp implements `localk up`: thin wrapper around `docker compose -f
// <file> up`. We don't auto-regenerate — the workflow is always
// `generate` then `up`. Trying to be too clever (regen-on-up) makes
// failures harder to reason about (which side broke?).
func runUp(args []string) {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	outDir := fs.String("out-dir", ".", "directory containing the generated docker-compose.yml")
	composeFile := fs.String("f", "docker-compose.yml", "compose file path; relative paths are resolved against --out-dir")
	build := fs.Bool("build", false, "rebuild images before starting (passes --build to docker compose)")
	noDetach := fs.Bool("no-detach", false, "keep the foreground attached (default: detach so the terminal returns)")
	disable := fs.String("disable", "", "comma-separated list of services to skip for this run (additive to the sticky `localk disable` list)")

	args, passthrough := splitDoubleDash(args)
	args = reorderFlagsFirst(args)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	path, err := resolveExistingCompose(*outDir, *composeFile)
	if err != nil {
		fail("%v", err)
	}
	if err := requireDockerCompose(); err != nil {
		fail("%v", err)
	}

	dcArgs := []string{"compose", "-f", path}
	dcArgs = appendDevOverlayIfPresent(dcArgs, *outDir)
	dcArgs = appendDisableOverlayIfPresent(dcArgs, *outDir)
	dcArgs = append(dcArgs, "up")
	if !*noDetach {
		dcArgs = append(dcArgs, "-d")
	}
	if *build {
		dcArgs = append(dcArgs, "--build")
	}
	// Transient disables: --scale <name>=0 keeps the service definition
	// in compose but starts zero replicas. Stacks with the sticky
	// overlay (which uses profiles) — both apply.
	for _, s := range splitCommaList(*disable) {
		dcArgs = append(dcArgs, "--scale", s+"=0")
	}
	dcArgs = append(dcArgs, passthrough...)

	execDocker(dcArgs)
}

// runDown implements `localk down`: thin wrapper around `docker compose
// -f <file> down`. Volume removal is opt-in via -v / --volumes because
// it permanently deletes data — the same semantics docker compose itself
// uses for that flag.
func runDown(args []string) {
	fs := flag.NewFlagSet("down", flag.ExitOnError)
	outDir := fs.String("out-dir", ".", "directory containing the generated docker-compose.yml")
	composeFile := fs.String("f", "docker-compose.yml", "compose file path; relative paths are resolved against --out-dir")
	volumes := fs.Bool("v", false, "also remove named volumes declared in the compose file (DESTRUCTIVE — deletes your local data)")
	fs.BoolVar(volumes, "volumes", false, "alias of -v")

	args, passthrough := splitDoubleDash(args)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	path, err := resolveExistingCompose(*outDir, *composeFile)
	if err != nil {
		fail("%v", err)
	}
	if err := requireDockerCompose(); err != nil {
		fail("%v", err)
	}

	dcArgs := []string{"compose", "-f", path}
	dcArgs = appendDevOverlayIfPresent(dcArgs, *outDir)
	dcArgs = appendDisableOverlayIfPresent(dcArgs, *outDir)
	dcArgs = append(dcArgs, "down")
	if *volumes {
		dcArgs = append(dcArgs, "-v")
	}
	dcArgs = append(dcArgs, passthrough...)

	execDocker(dcArgs)
}

// appendDevOverlayIfPresent extends dcArgs with `-f <overlay>` when
// docker-compose.dev.yml exists alongside the base compose file.
// Presence is the signal — no flag required — so `localk up` keeps
// honoring whatever the developer has set up via `localk dev` without
// extra ceremony.
func appendDevOverlayIfPresent(dcArgs []string, outDir string) []string {
	overlay := filepath.Join(outDir, overlayFilename)
	if _, err := os.Stat(overlay); err == nil {
		dcArgs = append(dcArgs, "-f", overlay)
	}
	return dcArgs
}

// appendDisableOverlayIfPresent does the same thing for the disable
// overlay (sticky list of services that should not start). Stacks
// cleanly with the dev overlay — compose merges as many `-f` files as
// you point at it.
func appendDisableOverlayIfPresent(dcArgs []string, outDir string) []string {
	overlay := filepath.Join(outDir, disableFilename)
	if _, err := os.Stat(overlay); err == nil {
		dcArgs = append(dcArgs, "-f", overlay)
	}
	return dcArgs
}

// splitCommaList splits a comma-separated flag value, trimming spaces
// and dropping empty entries. Used for `localk up --disable foo,bar`
// where users naturally write the list with spaces around the commas.
func splitCommaList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			seg := s[start:i]
			start = i + 1
			// trim spaces
			for len(seg) > 0 && (seg[0] == ' ' || seg[0] == '\t') {
				seg = seg[1:]
			}
			for len(seg) > 0 && (seg[len(seg)-1] == ' ' || seg[len(seg)-1] == '\t') {
				seg = seg[:len(seg)-1]
			}
			if seg != "" {
				out = append(out, seg)
			}
		}
	}
	return out
}

// resolveExistingCompose resolves a compose file path the same way
// `generate` does (absolute wins, otherwise joined with out-dir) but ALSO
// asserts the file exists, with a clear error pointing at `localk
// generate` if it doesn't.
func resolveExistingCompose(outDir, file string) (string, error) {
	path := resolveOutPath(outDir, file)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			abs, _ := filepath.Abs(path)
			return "", fmt.Errorf("compose file not found at %s\n  run `localk generate <input>` first, or pass -f to point at an existing file", abs)
		}
		return "", fmt.Errorf("checking %s: %w", path, err)
	}
	return path, nil
}

// requireDockerCompose checks that the docker CLI is on PATH and that the
// compose subcommand is available. We don't fall back to legacy
// docker-compose — v2 (the plugin) has been the default for years.
func requireDockerCompose() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker: not found on PATH (install Docker Desktop or the docker CLI)")
	}
	cmd := exec.Command("docker", "compose", "version")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose: not available — make sure Docker Desktop is running and the compose plugin is installed")
	}
	return nil
}

// execDocker runs `docker <args>` with stdio inherited so the user sees
// the live output. We replace the current process semantically by
// forwarding the exit code; this avoids the "double prompt" effect when
// up runs interactively.
func execDocker(args []string) {
	cmd := exec.Command("docker", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if asExitError(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fail("docker compose: %v", err)
	}
}

// asExitError is a small wrapper around errors.As to keep the import set
// in this file minimal. It returns true if err is (or wraps) an
// *exec.ExitError, populating target.
func asExitError(err error, target **exec.ExitError) bool {
	for err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			*target = ee
			return true
		}
		type unwrapper interface{ Unwrap() error }
		if u, ok := err.(unwrapper); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return false
}

// splitDoubleDash splits args at the first `--` (if present). Everything
// before is parsed by our flag set; everything after is passed verbatim
// to docker compose. Lets users do `localk up -- --remove-orphans` or
// `localk down -- --timeout 5` without us needing to model every flag
// docker compose accepts.
func splitDoubleDash(args []string) (own, passthrough []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}
