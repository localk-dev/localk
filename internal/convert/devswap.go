package convert

import (
	"fmt"
	"strings"

	"github.com/localk-dev/localk/internal/compose"
)

// devSwapRule replaces a production chart image (typically a Bitnami
// helm chart's clustered StatefulSet) with a vanilla upstream image
// for local dev. The chart's bootstrap loop assumes multiple pods
// coordinating via headless DNS — none of which works under compose —
// so the cleaner cut is to ditch the chart logic entirely and run a
// plain standalone instance that speaks the same wire protocol.
//
// Each rule:
//   - matches a workload by image prefix + a cluster-mode env signal
//     (so a non-clustered Bitnami image is left alone)
//   - swaps to a lean dev image that runs as a single node
//   - translates auth/connection env vars from chart-specific names
//     to the upstream image's names so app connection strings still
//     work (MONGODB_ROOT_USER → MONGO_INITDB_ROOT_USERNAME, etc.)
//   - drops the workload's init containers (chart-specific bootstraps
//     that have no equivalent in the vanilla image)
//   - clears command/entrypoint and chart-specific bind mounts so
//     compose doesn't try to inject scripts that no longer exist
type devSwapRule struct {
	// Name is human-readable; printed in the warning so users know
	// which rule fired.
	name string
	// MatchImage returns true when the workload's image is one this
	// rule knows how to swap.
	matchImage func(image string) bool
	// MatchEnv returns true when the env signals the cluster-mode
	// setup that motivates the swap. A Bitnami image alone isn't
	// enough — many users run Bitnami images as plain standalones.
	matchEnv func(env map[string]string) bool
	// DevImage is the vanilla replacement.
	devImage string
	// TransformService rewrites env / volumes / command on the
	// workload's main service to match what the dev image expects.
	transformService func(svc *compose.Service)
}

var devSwapRules = []devSwapRule{
	{
		name: "Bitnami MongoDB clustered chart → mongo:7 standalone",
		matchImage: func(img string) bool {
			return imageMatches(img, "bitnami/mongodb", "bitnamilegacy/mongodb")
		},
		matchEnv: func(env map[string]string) bool {
			// Replica-set mode signals are the canonical cluster
			// bootstrap markers — they're set by the helm chart only
			// when running as a StatefulSet replica set.
			return env["MONGODB_REPLICA_SET_MODE"] != "" ||
				env["MONGODB_REPLICA_SET_NAME"] != "" ||
				env["MONGODB_INITIAL_PRIMARY_HOST"] != ""
		},
		devImage:         "mongo:7",
		transformService: standaloneMongo,
	},
	{
		name: "Bitnami RabbitMQ clustered chart → rabbitmq:3-management standalone",
		matchImage: func(img string) bool {
			return imageMatches(img, "bitnami/rabbitmq", "bitnamilegacy/rabbitmq")
		},
		matchEnv: func(env map[string]string) bool {
			// USE_LONGNAME=true together with an `@` in NODE_NAME is
			// the chart's clustering setup; USE_LONGNAME alone or a
			// short node name would be a non-clustered config.
			return env["RABBITMQ_USE_LONGNAME"] == "true" ||
				strings.Contains(env["RABBITMQ_NODE_NAME"], "@")
		},
		devImage:         "rabbitmq:3-management",
		transformService: standaloneRabbit,
	},
}

// applyDevSwap walks the dev-swap rules in order and applies the first
// one that matches the workload. Returns the warning to surface, or ""
// when no rule fired or the user opted out via preserve_image.
//
// The workload's init containers are dropped here too (they're
// chart-specific bootstraps that the vanilla image doesn't need or
// understand). main's depends_on entries pointing at those inits are
// cleared so compose doesn't wait forever for services that no
// longer exist.
func applyDevSwap(svcName string, main *compose.Service, extras map[string]compose.Service, preserveImage bool) string {
	if preserveImage {
		return ""
	}
	envMap := main.Environment
	if envMap == nil {
		envMap = map[string]string{}
	}
	for _, rule := range devSwapRules {
		if !rule.matchImage(main.Image) {
			continue
		}
		if !rule.matchEnv(envMap) {
			continue
		}
		original := main.Image
		main.Image = rule.devImage
		if rule.transformService != nil {
			rule.transformService(main)
		}
		dropInitContainers(svcName, main, extras)
		return fmt.Sprintf(
			"%s detected on workload %q: replaced image %q with %q for local dev. The chart's clustered bootstrap doesn't translate to compose; the dev image runs as a single standalone node speaking the same wire protocol. Set services.%s.preserve_image: true in localk.yaml to keep the chart image.",
			rule.name, svcName, original, rule.devImage, svcName,
		)
	}
	return ""
}

// dropInitContainers removes init-container extras owned by this
// workload (their compose service names are prefixed with the
// workload name and they have Restart="no"). Sidecars stay — those
// are user-meaningful even with the swapped image.
func dropInitContainers(svcName string, main *compose.Service, extras map[string]compose.Service) {
	for name, svc := range extras {
		if !strings.HasPrefix(name, svcName+"-") {
			continue
		}
		if svc.Restart != "no" {
			continue
		}
		delete(extras, name)
		// Clear depends_on entries pointing at this init so main
		// doesn't wait forever on a service that no longer exists.
		delete(main.DependsOn, name)
	}
	if len(main.DependsOn) == 0 {
		main.DependsOn = nil
	}
}

// imageMatches checks whether `image` references any of the given
// repository prefixes, ignoring registry host (`docker.io/`,
// `registry-1.docker.io/`) and tag.
func imageMatches(image string, prefixes ...string) bool {
	bare := strings.TrimPrefix(image, "docker.io/")
	bare = strings.TrimPrefix(bare, "registry-1.docker.io/")
	if i := strings.IndexByte(bare, ':'); i >= 0 {
		bare = bare[:i]
	}
	for _, p := range prefixes {
		if bare == p || strings.HasPrefix(bare, p+"/") {
			return true
		}
	}
	return false
}

// standaloneMongo rewrites a Bitnami-mongodb compose service into a
// shape that vanilla mongo:7 actually runs:
//
//   - Translates MONGODB_ROOT_USER/PASSWORD into the
//     MONGO_INITDB_ROOT_USERNAME/PASSWORD names mongo:7's entrypoint
//     reads on first boot.
//   - Drops every other MONGODB_*, MY_POD_*, K8S_*, BITNAMI_* env so
//     the vanilla image sees only what it understands. (Mongo:7 just
//     ignores unknown env, but a clean compose file is easier to
//     reason about.)
//   - Drops chart-specific volume mounts (configs/scripts directories
//     that the chart pre-populates and the standalone image has no
//     use for). The PVC-backed datadir survives.
//   - Clears command/entrypoint — the upstream image's defaults
//     (`mongod --bind_ip_all`) are exactly what we want.
func standaloneMongo(svc *compose.Service) {
	if user := svc.Environment["MONGODB_ROOT_USER"]; user != "" {
		svc.Environment["MONGO_INITDB_ROOT_USERNAME"] = user
	}
	if pass := svc.Environment["MONGODB_ROOT_PASSWORD"]; pass != "" {
		svc.Environment["MONGO_INITDB_ROOT_PASSWORD"] = pass
	}
	stripPrefixes(svc.Environment, "MONGODB_", "MY_POD_", "K8S_", "BITNAMI_")
	// /bitnami is Bitnami's umbrella prefix for everything chart-
	// specific (configs, scripts, helpers); /opt/bitnami/mongodb is
	// the runtime layout the chart wires up. Drop both — vanilla
	// mongo doesn't use them and the bind sources won't exist after
	// the swap anyway.
	svc.Volumes = filterChartVolumes(svc.Volumes, "/bitnami", "/opt/bitnami/mongodb")
	svc.Command = nil
	svc.Entrypoint = nil
}

// standaloneRabbit is the rabbitmq-3-management equivalent of
// standaloneMongo. RABBITMQ_USERNAME/PASSWORD become
// RABBITMQ_DEFAULT_USER/PASS (the names the upstream entrypoint
// reads). Cluster/node-name env vars are dropped so the Erlang VM
// uses its default short-name binding.
func standaloneRabbit(svc *compose.Service) {
	if user := svc.Environment["RABBITMQ_USERNAME"]; user != "" {
		svc.Environment["RABBITMQ_DEFAULT_USER"] = user
	}
	if pass := svc.Environment["RABBITMQ_PASSWORD"]; pass != "" {
		svc.Environment["RABBITMQ_DEFAULT_PASS"] = pass
	}
	// Drop Bitnami-specific RABBITMQ_* env (everything except the
	// DEFAULT_USER/PASS we just set). Also drop downward-API and
	// chart-internal env that the vanilla image has no use for.
	for k := range svc.Environment {
		if strings.HasPrefix(k, "RABBITMQ_") &&
			k != "RABBITMQ_DEFAULT_USER" &&
			k != "RABBITMQ_DEFAULT_PASS" {
			delete(svc.Environment, k)
		}
	}
	stripPrefixes(svc.Environment, "MY_POD_", "K8S_", "BITNAMI_")
	svc.Volumes = filterChartVolumes(svc.Volumes, "/bitnami", "/opt/bitnami/rabbitmq")
	svc.Command = nil
	svc.Entrypoint = nil
}

// stripPrefixes deletes any env keys that start with one of the
// given prefixes. Used to clean chart-specific cruft after an image
// swap.
func stripPrefixes(env map[string]string, prefixes ...string) {
	for k := range env {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				delete(env, k)
				break
			}
		}
	}
}

// filterChartVolumes drops mounts whose target path lives under any
// of the given chart-specific roots. Keeps everything else, so PVC
// data volumes (mounted at vanilla paths like /data/db) survive.
func filterChartVolumes(volumes []any, chartRoots ...string) []any {
	out := make([]any, 0, len(volumes))
	for _, v := range volumes {
		s, ok := v.(string)
		if !ok {
			out = append(out, v)
			continue
		}
		if matchesChartRoot(s, chartRoots...) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// matchesChartRoot returns true when a short-form mount string's
// container-side path falls under any of the chart roots. Compose's
// short form is "[source:]target[:mode]"; we extract the target.
func matchesChartRoot(mount string, roots ...string) bool {
	target := mount
	if i := strings.IndexByte(mount, ':'); i >= 0 {
		target = mount[i+1:]
	}
	if i := strings.IndexByte(target, ':'); i >= 0 {
		target = target[:i]
	}
	for _, r := range roots {
		if target == r || strings.HasPrefix(target, r+"/") {
			return true
		}
	}
	return false
}
