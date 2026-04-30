package convert

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/localk-dev/localk/internal/compose"
)

// ResourceMode controls what RebalanceResources does to memory or CPU
// limits. Default is auto (scale-to-fit-Docker), but users on tiny
// stacks may want preserve, and users who don't care about caps may
// want drop.
type ResourceMode string

const (
	// ResourceAuto: detect Docker's available memory/CPU, scale all
	// declared limits proportionally if their sum exceeds a safety
	// fraction (default 80%) of available. Preserves relative weights
	// from the k8s manifest.
	ResourceAuto ResourceMode = "auto"

	// ResourcePreserve: emit limits exactly as declared in the k8s
	// manifests. The output may ask for more memory/CPU than the host
	// can deliver — fine if you're aware, painful if you're not.
	ResourcePreserve ResourceMode = "preserve"

	// ResourceDrop: remove all limits from the compose output. The
	// kernel arbitrates based on actual demand. Closest to "dev
	// laptop" semantics for stacks where the limits are prod
	// scheduling hints rather than something you want enforced
	// locally.
	ResourceDrop ResourceMode = "drop"
)

// safetyFraction is the share of Docker's reported total we target as
// the upper bound for the scaled sum. 0.8 leaves headroom for the
// kernel + other host workloads + bursts.
const safetyFraction = 0.8

// Probe abstracts away `docker info` so tests can drive
// RebalanceResources with fixed values. Implementations return
// (value, true) when the info is available, or (0, false) when
// Docker isn't reachable — generation still works in both cases.
type Probe interface {
	MemoryBytes() (int64, bool)
	CPUCount() (int, bool)
}

// DockerProbe is the production implementation: shells out to
// `docker info --format '{{.MemTotal}}'` / `{{.NCPU}}`.
type DockerProbe struct{}

func (DockerProbe) MemoryBytes() (int64, bool) {
	out, err := exec.Command("docker", "info", "--format", "{{.MemTotal}}").Output()
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func (DockerProbe) CPUCount() (int, bool) {
	out, err := exec.Command("docker", "info", "--format", "{{.NCPU}}").Output()
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// RebalanceResources mutates res's compose services to apply the
// requested memory and CPU policies. Appends warnings to res.Warnings
// describing what changed; the caller surfaces them through the same
// channel as the rest of the conversion warnings.
//
// Mode "" is treated as ResourceAuto so callers can leave the field
// zero-valued for default behavior.
func RebalanceResources(res *Result, memMode, cpuMode ResourceMode, probe Probe) {
	rebalanceMemory(res, normalizeMode(memMode), probe)
	rebalanceCPU(res, normalizeMode(cpuMode), probe)
}

func normalizeMode(m ResourceMode) ResourceMode {
	switch m {
	case "", ResourceAuto:
		return ResourceAuto
	case ResourcePreserve, ResourceDrop:
		return m
	}
	return ResourceAuto // unknown → auto, the safe default
}

func rebalanceMemory(res *Result, mode ResourceMode, probe Probe) {
	switch mode {
	case ResourcePreserve:
		return
	case ResourceDrop:
		dropped := clearMemoryLimits(res.Compose.Services)
		if dropped > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"--memory=drop: removed memory limits from %d service(s)", dropped,
			))
		}
		return
	}

	// Auto mode: probe Docker, scale to fit if needed.
	total, ok := probe.MemoryBytes()
	if !ok {
		return // no docker info available — leave limits alone
	}
	sum := sumMemoryLimits(res.Compose.Services)
	if sum == 0 {
		return // nothing to scale
	}
	target := int64(float64(total) * safetyFraction)
	if sum <= target {
		return // already fits — no change
	}
	factor := float64(target) / float64(sum)
	applyMemoryFactor(res.Compose.Services, factor)
	res.Warnings = append(res.Warnings, fmt.Sprintf(
		"memory limits scaled to %.1f%% (factor %.3f) to fit Docker's %s available — declared total %s, scaled to %s. Pass --memory=preserve to keep declared values, --memory=drop to remove limits entirely.",
		factor*100, factor,
		humanBytes(total), humanBytes(sum), humanBytes(int64(float64(sum)*factor)),
	))
}

func rebalanceCPU(res *Result, mode ResourceMode, probe Probe) {
	switch mode {
	case ResourcePreserve:
		return
	case ResourceDrop:
		dropped := clearCPULimits(res.Compose.Services)
		if dropped > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"--cpu=drop: removed CPU limits from %d service(s)", dropped,
			))
		}
		return
	}

	cores, ok := probe.CPUCount()
	if !ok {
		return
	}
	sum := sumCPULimits(res.Compose.Services)
	if sum == 0 {
		return
	}
	target := float64(cores) * safetyFraction
	if sum <= target {
		return
	}
	factor := target / sum
	applyCPUFactor(res.Compose.Services, factor)
	res.Warnings = append(res.Warnings, fmt.Sprintf(
		"CPU limits scaled to %.1f%% (factor %.3f) to fit Docker's %d cores — declared total %.2f cores, scaled to %.2f. Pass --cpu=preserve to keep declared values, --cpu=drop to remove limits entirely.",
		factor*100, factor, cores, sum, sum*factor,
	))
}

func sumMemoryLimits(services map[string]compose.Service) int64 {
	var sum int64
	for _, s := range services {
		if s.Deploy == nil || s.Deploy.Resources.Limits == nil {
			continue
		}
		if n, err := strconv.ParseInt(s.Deploy.Resources.Limits.Memory, 10, 64); err == nil {
			sum += n
		}
	}
	return sum
}

func sumCPULimits(services map[string]compose.Service) float64 {
	var sum float64
	for _, s := range services {
		if s.Deploy == nil || s.Deploy.Resources.Limits == nil {
			continue
		}
		if v, err := strconv.ParseFloat(s.Deploy.Resources.Limits.CPUs, 64); err == nil {
			sum += v
		}
	}
	return sum
}

func applyMemoryFactor(services map[string]compose.Service, factor float64) {
	for name, s := range services {
		if s.Deploy == nil || s.Deploy.Resources.Limits == nil {
			continue
		}
		n, err := strconv.ParseInt(s.Deploy.Resources.Limits.Memory, 10, 64)
		if err != nil {
			continue
		}
		s.Deploy.Resources.Limits.Memory = strconv.FormatInt(int64(float64(n)*factor), 10)
		services[name] = s
	}
}

func applyCPUFactor(services map[string]compose.Service, factor float64) {
	for name, s := range services {
		if s.Deploy == nil || s.Deploy.Resources.Limits == nil {
			continue
		}
		v, err := strconv.ParseFloat(s.Deploy.Resources.Limits.CPUs, 64)
		if err != nil {
			continue
		}
		s.Deploy.Resources.Limits.CPUs = fmt.Sprintf("%.3f", v*factor)
		services[name] = s
	}
}

func clearMemoryLimits(services map[string]compose.Service) int {
	count := 0
	for name, s := range services {
		if s.Deploy == nil || s.Deploy.Resources.Limits == nil {
			continue
		}
		if s.Deploy.Resources.Limits.Memory == "" {
			continue
		}
		s.Deploy.Resources.Limits.Memory = ""
		// If both fields end up empty, drop the Limits struct so the
		// generated YAML stays tidy.
		if s.Deploy.Resources.Limits.CPUs == "" {
			s.Deploy.Resources.Limits = nil
			if s.Deploy.Resources.Reservations == nil {
				s.Deploy = nil
			}
		}
		services[name] = s
		count++
	}
	return count
}

func clearCPULimits(services map[string]compose.Service) int {
	count := 0
	for name, s := range services {
		if s.Deploy == nil || s.Deploy.Resources.Limits == nil {
			continue
		}
		if s.Deploy.Resources.Limits.CPUs == "" {
			continue
		}
		s.Deploy.Resources.Limits.CPUs = ""
		if s.Deploy.Resources.Limits.Memory == "" {
			s.Deploy.Resources.Limits = nil
			if s.Deploy.Resources.Reservations == nil {
				s.Deploy = nil
			}
		}
		services[name] = s
		count++
	}
	return count
}

// humanBytes formats a byte count like docker / k8s do: "16.0 GB",
// "512.0 MB", etc. Used in warning messages so the user can eyeball
// the scaling decision.
func humanBytes(n int64) string {
	const (
		kb = 1000
		mb = 1000 * 1000
		gb = 1000 * 1000 * 1000
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
