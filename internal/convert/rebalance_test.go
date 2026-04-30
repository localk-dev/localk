package convert

import (
	"strconv"
	"strings"
	"testing"

	"github.com/localk-dev/localk/internal/compose"
)

// fakeProbe is the test double for DockerProbe. Lets each test
// declare exactly what Docker would have reported, including the
// "not available" case.
type fakeProbe struct {
	mem   int64
	memOK bool
	cpu   int
	cpuOK bool
}

func (f fakeProbe) MemoryBytes() (int64, bool) { return f.mem, f.memOK }
func (f fakeProbe) CPUCount() (int, bool)      { return f.cpu, f.cpuOK }

// TestRebalance_MemoryAutoScalesDown is the headline case: declared
// limits sum to more than 80% of Docker's available memory, so we
// scale every limit by the same factor. Preserves the relative
// weights from the k8s manifest.
func TestRebalance_MemoryAutoScalesDown(t *testing.T) {
	res := &Result{Compose: &compose.File{Services: map[string]compose.Service{
		"big":    {Deploy: limitDeploy("32000000000", "")}, // 32 GB
		"medium": {Deploy: limitDeploy("8000000000", "")},  // 8 GB
		"small":  {Deploy: limitDeploy("1000000000", "")},  // 1 GB
	}}}
	// 16 GB available → target = 12.8 GB. Sum is 41 GB. Factor ≈ 0.312.
	probe := fakeProbe{mem: 16_000_000_000, memOK: true}

	RebalanceResources(res, ResourceAuto, ResourceAuto, probe)

	got := mustParseInt64(t, res.Compose.Services["big"].Deploy.Resources.Limits.Memory)
	if got > 13_000_000_000 || got < 9_000_000_000 {
		t.Errorf("big should be roughly 32GB * 0.312 ≈ 10GB; got %d (%.1f GB)", got, float64(got)/1e9)
	}
	// Relative weights preserved (within float-rounding noise): big
	// should still be ~4x medium, medium ~8x small. Allow 0.1% drift
	// for the integer truncation that happens when bytes is converted
	// from float.
	bigBytes := mustParseInt64(t, res.Compose.Services["big"].Deploy.Resources.Limits.Memory)
	mediumBytes := mustParseInt64(t, res.Compose.Services["medium"].Deploy.Resources.Limits.Memory)
	smallBytes := mustParseInt64(t, res.Compose.Services["small"].Deploy.Resources.Limits.Memory)
	if !approxRatio(float64(bigBytes)/float64(mediumBytes), 4.0, 0.001) {
		t.Errorf("relative weight broken: big should be ~4x medium; ratio %.4f", float64(bigBytes)/float64(mediumBytes))
	}
	if !approxRatio(float64(mediumBytes)/float64(smallBytes), 8.0, 0.001) {
		t.Errorf("relative weight broken: medium should be ~8x small; ratio %.4f", float64(mediumBytes)/float64(smallBytes))
	}

	// A descriptive warning gets emitted.
	if !hasWarning(res, "memory limits scaled") {
		t.Errorf("expected scaling warning; got: %v", res.Warnings)
	}
}

// TestRebalance_MemoryAutoNoOpWhenAlreadyFits skips the scaling pass
// when the declared sum is already under the safety threshold. No
// modification, no warning.
func TestRebalance_MemoryAutoNoOpWhenAlreadyFits(t *testing.T) {
	res := &Result{Compose: &compose.File{Services: map[string]compose.Service{
		"a": {Deploy: limitDeploy("500000000", "")},  // 500 MB
		"b": {Deploy: limitDeploy("1000000000", "")}, // 1 GB
	}}}
	probe := fakeProbe{mem: 16_000_000_000, memOK: true}

	RebalanceResources(res, ResourceAuto, ResourceAuto, probe)

	if got := res.Compose.Services["a"].Deploy.Resources.Limits.Memory; got != "500000000" {
		t.Errorf("limit should be unchanged when sum already fits; got %s", got)
	}
	if hasWarning(res, "scaled") {
		t.Errorf("no warning expected when no scaling happens; got: %v", res.Warnings)
	}
}

// TestRebalance_MemoryDropClearsAll covers --memory=drop: every
// memory limit is removed.
func TestRebalance_MemoryDropClearsAll(t *testing.T) {
	res := &Result{Compose: &compose.File{Services: map[string]compose.Service{
		"a": {Deploy: limitDeploy("500000000", "0.5")}, // mem AND cpu
		"b": {Deploy: limitDeploy("1000000000", "")},   // mem only
	}}}
	probe := fakeProbe{} // probe shouldn't be called for drop

	RebalanceResources(res, ResourceDrop, ResourcePreserve, probe)

	// Mem dropped on both. CPU on `a` preserved.
	a := res.Compose.Services["a"]
	if a.Deploy == nil || a.Deploy.Resources.Limits == nil {
		t.Fatalf("a's Deploy should still exist (CPU limit retained); got %+v", a.Deploy)
	}
	if a.Deploy.Resources.Limits.Memory != "" {
		t.Errorf("a.Memory = %q, want empty", a.Deploy.Resources.Limits.Memory)
	}
	if a.Deploy.Resources.Limits.CPUs != "0.5" {
		t.Errorf("a.CPUs = %q, want 0.5 (preserved)", a.Deploy.Resources.Limits.CPUs)
	}
	// b had only memory; with that gone, the entire Deploy struct
	// should drop so the YAML stays tidy.
	b := res.Compose.Services["b"]
	if b.Deploy != nil {
		t.Errorf("b.Deploy should be nil after dropping the only limit; got %+v", b.Deploy)
	}
}

// TestRebalance_PreserveSkips ensures preserve mode never modifies
// anything regardless of probe results. The escape hatch for users
// who deliberately want raw k8s values.
func TestRebalance_PreserveSkips(t *testing.T) {
	res := &Result{Compose: &compose.File{Services: map[string]compose.Service{
		"big": {Deploy: limitDeploy("32000000000", "10")},
	}}}
	probe := fakeProbe{mem: 1_000_000, memOK: true} // tiny — would force scaling under auto

	RebalanceResources(res, ResourcePreserve, ResourcePreserve, probe)

	if got := res.Compose.Services["big"].Deploy.Resources.Limits.Memory; got != "32000000000" {
		t.Errorf("preserve mode should not modify memory; got %s", got)
	}
	if got := res.Compose.Services["big"].Deploy.Resources.Limits.CPUs; got != "10" {
		t.Errorf("preserve mode should not modify CPUs; got %s", got)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("preserve mode should not emit warnings; got %v", res.Warnings)
	}
}

// TestRebalance_NoProbeAvailable simulates Docker not running. Auto
// mode falls back to leaving limits alone — generation works
// offline.
func TestRebalance_NoProbeAvailable(t *testing.T) {
	res := &Result{Compose: &compose.File{Services: map[string]compose.Service{
		"big": {Deploy: limitDeploy("32000000000", "10")},
	}}}
	probe := fakeProbe{} // memOK=false, cpuOK=false

	RebalanceResources(res, ResourceAuto, ResourceAuto, probe)

	if got := res.Compose.Services["big"].Deploy.Resources.Limits.Memory; got != "32000000000" {
		t.Errorf("limits should be preserved when Docker isn't available; got %s", got)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("no warning expected when probe unavailable; got %v", res.Warnings)
	}
}

// TestRebalance_CPUAutoScalesDown mirrors the memory case for CPU.
func TestRebalance_CPUAutoScalesDown(t *testing.T) {
	res := &Result{Compose: &compose.File{Services: map[string]compose.Service{
		"big":    {Deploy: limitDeploy("", "10")},
		"medium": {Deploy: limitDeploy("", "5")},
		"small":  {Deploy: limitDeploy("", "1")},
	}}}
	// 8 cores → target 6.4. Sum is 16. Factor = 0.4.
	probe := fakeProbe{cpu: 8, cpuOK: true}

	RebalanceResources(res, ResourcePreserve, ResourceAuto, probe)

	bigCPU := mustParseFloat(t, res.Compose.Services["big"].Deploy.Resources.Limits.CPUs)
	if bigCPU > 4.5 || bigCPU < 3.5 {
		t.Errorf("big should be roughly 10 * 0.4 = 4.0; got %.3f", bigCPU)
	}
	if !hasWarning(res, "CPU limits scaled") {
		t.Errorf("expected CPU scaling warning; got: %v", res.Warnings)
	}
}

// TestNormalizeMode collapses unknown / empty modes to auto.
func TestNormalizeMode(t *testing.T) {
	cases := []struct {
		in   ResourceMode
		want ResourceMode
	}{
		{"", ResourceAuto},
		{ResourceAuto, ResourceAuto},
		{ResourcePreserve, ResourcePreserve},
		{ResourceDrop, ResourceDrop},
		{"nonsense", ResourceAuto}, // fallback to safe default
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			if got := normalizeMode(tc.in); got != tc.want {
				t.Errorf("normalizeMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHumanBytes verifies the formatting used in scaling warnings —
// users seeing "16.0 GB" recognize their host immediately.
func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{16_000_000_000, "16.0 GB"},
		{512_000_000, "512.0 MB"},
		{1_500, "1.5 KB"},
		{42, "42 B"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// limitDeploy is a small helper that builds a *compose.Deploy with
// the given memory and CPU limits, leaving the empty side empty.
func limitDeploy(memory, cpus string) *compose.Deploy {
	return &compose.Deploy{
		Resources: compose.DeployResources{
			Limits: &compose.ResourceSpec{Memory: memory, CPUs: cpus},
		},
	}
}

func mustParseInt64(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("ParseInt(%q): %v", s, err)
	}
	return n
}

func mustParseFloat(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("ParseFloat(%q): %v", s, err)
	}
	return v
}

// approxRatio returns true when |actual - expected| / expected is
// within tolerance. Used to assert "ratio preserved" without
// requiring bit-exact equality after a float multiply + integer
// truncation pipeline.
func approxRatio(actual, expected, tolerance float64) bool {
	if expected == 0 {
		return actual == 0
	}
	diff := actual - expected
	if diff < 0 {
		diff = -diff
	}
	return diff/expected <= tolerance
}

func hasWarning(res *Result, contains string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w, contains) {
			return true
		}
	}
	return false
}
