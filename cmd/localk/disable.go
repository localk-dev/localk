package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/localk-dev/localk/internal/devmode"
)

const disableFilename = "docker-compose.disable.yml"

// runDisable implements `localk disable`. Three modes:
//   - localk disable <s1> <s2> ...    add to sticky list
//   - localk disable --restore <s>     remove one entry
//   - localk disable --clear           empty the list
//   - localk disable --list            print current state
//
// The sticky list lives in docker-compose.disable.yml as a compose
// overlay using the `profiles: ["disabled"]` mechanism — services
// tagged with a profile only start when that profile is activated,
// and we never activate this one. Plain `docker compose up` skips
// them automatically.
func runDisable(args []string) {
	fs := flag.NewFlagSet("disable", flag.ExitOnError)
	outDir := fs.String("out-dir", ".", "directory containing the generated docker-compose.yml")
	restore := fs.String("restore", "", "remove one service from the disable list")
	clear := fs.Bool("clear", false, "remove every service from the disable list")
	list := fs.Bool("list", false, "show services currently disabled")

	args = reorderFlagsFirst(args)
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	disablePath := filepath.Join(*outDir, disableFilename)

	switch {
	case *list:
		runDisableList(disablePath)
	case *clear:
		runDisableClear(disablePath)
	case *restore != "":
		runDisableRestore(*restore, disablePath)
	default:
		if fs.NArg() == 0 {
			fmt.Fprintln(os.Stderr, "disable: name at least one service to disable")
			fmt.Fprintln(os.Stderr, "usage:")
			fmt.Fprintln(os.Stderr, "  localk disable <service> [<service> ...]   [--out-dir <dir>]")
			fmt.Fprintln(os.Stderr, "  localk disable --restore <service>         [--out-dir <dir>]")
			fmt.Fprintln(os.Stderr, "  localk disable --clear                     [--out-dir <dir>]")
			fmt.Fprintln(os.Stderr, "  localk disable --list                      [--out-dir <dir>]")
			os.Exit(2)
		}
		runDisableAdd(fs.Args(), *outDir, disablePath)
	}
}

func runDisableAdd(services []string, outDir, disablePath string) {
	base, err := loadBaseCompose(outDir)
	if err != nil {
		fail("%v", err)
	}

	// Reject services that don't exist in the base — almost certainly
	// a typo, and silently storing a non-matching name would leave the
	// user wondering why the stack is still starting it.
	var unknown []string
	for _, s := range services {
		if _, ok := base.Services[s]; !ok {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		known := serviceNames(base)
		fail("disable: unknown service(s): %s\n  known services: %s", joinComma(unknown), joinHints(known))
	}

	// Reject anything already in dev mode — dev expects the overlay's
	// proxy entry to start; disabling it contradicts that. Easier to
	// surface this than have the user debug a stack that won't come up.
	devOverlayPath := filepath.Join(outDir, overlayFilename)
	devOverlay, _, err := devmode.Load(devOverlayPath)
	if err != nil {
		fail("%v", err)
	}
	for _, s := range services {
		if _, inDev := devOverlay.Services[s]; inDev {
			fail("disable: %q is currently in dev mode (proxied via %s); run `localk dev --stop %s` first if you really want it disabled", s, devOverlayPath, s)
		}
	}

	d, _, err := devmode.LoadDisabled(disablePath)
	if err != nil {
		fail("%v", err)
	}
	added := 0
	for _, s := range services {
		if !d.IsDisabled(s) {
			added++
		}
		d.Add(s)
	}
	if err := d.Save(disablePath); err != nil {
		fail("%v", err)
	}

	if added == 0 {
		fmt.Printf("Nothing to do — all named services were already disabled.\n")
	} else {
		fmt.Printf("Disabled %d service(s): %s\n", added, joinComma(services))
	}
	fmt.Println()
	fmt.Printf("Run `localk up --out-dir %s` to (re-)apply. The disable overlay is auto-detected.\n", outDir)
}

func runDisableRestore(service, disablePath string) {
	d, _, err := devmode.LoadDisabled(disablePath)
	if err != nil {
		fail("%v", err)
	}
	if !d.Remove(service) {
		fail("disable --restore: %q wasn't disabled (overlay: %s)", service, disablePath)
	}
	if err := d.Save(disablePath); err != nil {
		fail("%v", err)
	}
	fmt.Printf("Restored %q.\n", service)
	if len(d.Services) == 0 {
		fmt.Printf("Removed empty %s.\n", disablePath)
	}
}

func runDisableClear(disablePath string) {
	d, exists, err := devmode.LoadDisabled(disablePath)
	if err != nil {
		fail("%v", err)
	}
	if !exists || len(d.Services) == 0 {
		fmt.Println("Nothing to clear — no services are currently disabled.")
		return
	}
	count := len(d.Services)
	d.Clear()
	if err := d.Save(disablePath); err != nil {
		fail("%v", err)
	}
	fmt.Printf("Cleared %d disabled service(s). Removed %s.\n", count, disablePath)
}

func runDisableList(disablePath string) {
	d, exists, err := devmode.LoadDisabled(disablePath)
	if err != nil {
		fail("%v", err)
	}
	if !exists || len(d.Services) == 0 {
		fmt.Println("No services are currently disabled.")
		return
	}
	fmt.Printf("Disabled services (overlay: %s):\n", disablePath)
	for _, n := range d.Names() {
		fmt.Printf("  %s\n", n)
	}
}
