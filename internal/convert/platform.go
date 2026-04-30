package convert

import (
	"fmt"

	"github.com/localk-dev/localk/internal/compose"
)

// PlatformMode controls whether ApplyPlatform pins each service's
// `platform:` field. The mode is a string rather than an enum so the
// CLI flag can pass through arbitrary literal values like
// "linux/amd64" or "linux/arm64" without us having to enumerate
// every Docker-supported platform.
type PlatformMode string

const (
	// PlatformAuto picks based on the host. On arm64 hosts (Apple
	// Silicon, ARM Linux) it pins to linux/amd64 because most
	// private registries ship amd64-only and pulling without a
	// platform pin yields "no matching manifest" errors. On amd64
	// hosts it does nothing (the host's native arch is already what
	// Docker pulls).
	PlatformAuto PlatformMode = "auto"

	// PlatformNative is the explicit no-op: never emit `platform:`.
	// Use when every image in your stack is multi-arch and you want
	// native performance on each host.
	PlatformNative PlatformMode = "native"
)

// ApplyPlatform mutates res's compose services per the requested
// mode and host architecture. hostArch is passed in so tests can
// drive both arm64 and amd64 paths without depending on
// runtime.GOARCH; the production caller passes runtime.GOARCH.
//
// Mode "" or PlatformAuto trigger host-aware behavior. Any other
// value (including PlatformNative) is interpreted as a literal
// platform string, except PlatformNative which is the documented
// "skip" sentinel.
func ApplyPlatform(res *Result, mode PlatformMode, hostArch string) {
	switch mode {
	case "", PlatformAuto:
		if hostArch == "arm64" {
			setPlatformOnAll(res.Compose.Services, "linux/amd64", res.SwappedServices)
			res.Warnings = append(res.Warnings,
				"platform pinned to linux/amd64 on every service (host is arm64; most private registries ship amd64-only and would fail with 'no matching manifest'). Pass --platform=native to skip pinning, --platform=<value> to override.",
			)
		}
		// On amd64 hosts: leave platform unset; Docker pulls native.
	case PlatformNative:
		// Explicit no-op — user opted out.
	default:
		// Literal platform string. We trust the user; Docker will
		// reject obviously bogus values.
		setPlatformOnAll(res.Compose.Services, string(mode), res.SwappedServices)
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"platform pinned to %s on every service (--platform=%s).",
			string(mode), string(mode),
		))
	}
}

// setPlatformOnAll writes platform onto every service, overwriting
// any existing value — except for services dev-swapped to a known
// multi-arch upstream image (mongo, rabbitmq, …). Pinning amd64 on
// those forces Rosetta emulation, which has known socket-syscall
// bugs (mongo:7's setup mongod fails bind() with EINVAL under
// emulation). The swap chose a multi-arch image specifically to
// avoid that path; respect the choice.
func setPlatformOnAll(services map[string]compose.Service, platform string, skip map[string]bool) {
	for name, s := range services {
		if skip[name] {
			continue
		}
		s.Platform = platform
		services[name] = s
	}
}
