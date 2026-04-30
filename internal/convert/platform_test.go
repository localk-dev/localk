package convert

import (
	"testing"

	"github.com/localk-dev/localk/internal/compose"
)

// TestApplyPlatform_AutoOnArm64 covers the headline case: M-series
// Mac user runs `localk generate` against a stack of private-
// registry amd64-only images. We pin every service to linux/amd64
// so the eventual `docker compose pull` doesn't fail.
func TestApplyPlatform_AutoOnArm64(t *testing.T) {
	res := newPlatformResult("api", "worker", "postgres")
	ApplyPlatform(res, PlatformAuto, "arm64")

	for _, name := range []string{"api", "worker", "postgres"} {
		if got := res.Compose.Services[name].Platform; got != "linux/amd64" {
			t.Errorf("services[%q].Platform = %q, want linux/amd64", name, got)
		}
	}
	if !hasWarning(res, "linux/amd64") {
		t.Errorf("expected warning describing the auto-pin; got %v", res.Warnings)
	}
}

// TestApplyPlatform_AutoOnAmd64 verifies amd64 hosts get no
// platform pinning — no "wasted" platform field cluttering the
// generated YAML and no forced emulation when the host already
// matches the registry's default arch.
func TestApplyPlatform_AutoOnAmd64(t *testing.T) {
	res := newPlatformResult("api")
	ApplyPlatform(res, PlatformAuto, "amd64")

	if got := res.Compose.Services["api"].Platform; got != "" {
		t.Errorf("amd64 host should not auto-pin; got Platform=%q", got)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("amd64 host should produce no warning; got %v", res.Warnings)
	}
}

// TestApplyPlatform_NativeIsExplicitOptOut covers users who have
// fully multi-arch registries and don't want pinning. Always a
// no-op regardless of host arch.
func TestApplyPlatform_NativeIsExplicitOptOut(t *testing.T) {
	res := newPlatformResult("api")
	ApplyPlatform(res, PlatformNative, "arm64")

	if got := res.Compose.Services["api"].Platform; got != "" {
		t.Errorf("native mode should not pin even on arm64; got Platform=%q", got)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("native mode should produce no warning; got %v", res.Warnings)
	}
}

// TestApplyPlatform_LiteralStringForcesValue passes a raw platform
// string through to every service. Lets users target unusual
// platforms (linux/arm/v7, linux/s390x, etc) without us having to
// enumerate them.
func TestApplyPlatform_LiteralStringForcesValue(t *testing.T) {
	res := newPlatformResult("api", "worker")
	ApplyPlatform(res, PlatformMode("linux/arm64"), "amd64")

	for _, name := range []string{"api", "worker"} {
		if got := res.Compose.Services[name].Platform; got != "linux/arm64" {
			t.Errorf("services[%q].Platform = %q, want linux/arm64", name, got)
		}
	}
	if !hasWarning(res, "linux/arm64") {
		t.Errorf("expected warning naming the literal platform; got %v", res.Warnings)
	}
}

// TestApplyPlatform_OverwritesExistingPlatform verifies setPlatformOnAll's
// overwrite semantic. If a service already had a platform (e.g.,
// from an earlier override layer), the user's --platform=<value>
// flag wins. Surprising-but-consistent: localk doesn't promise to
// preserve fields users haven't asked it to.
func TestApplyPlatform_OverwritesExistingPlatform(t *testing.T) {
	res := newPlatformResult("api")
	api := res.Compose.Services["api"]
	api.Platform = "linux/arm64"
	res.Compose.Services["api"] = api

	ApplyPlatform(res, PlatformMode("linux/amd64"), "amd64")

	if got := res.Compose.Services["api"].Platform; got != "linux/amd64" {
		t.Errorf("explicit --platform should overwrite; got %q", got)
	}
}

// newPlatformResult assembles a minimal Result with named services
// for platform tests. Each service has just an image set — enough
// for ApplyPlatform's per-service iteration to do its work.
func newPlatformResult(names ...string) *Result {
	services := map[string]compose.Service{}
	for _, n := range names {
		services[n] = compose.Service{Image: n + ":latest"}
	}
	return &Result{Compose: &compose.File{Services: services}}
}
