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
	// MatchEnv returns true when env signals (from either the
	// service's literal Environment OR the .env file we generated
	// for secret-derived values) indicate the cluster-mode setup
	// that motivates the swap. A Bitnami image alone isn't enough —
	// many users run Bitnami images as plain standalones.
	matchEnv func(lookup envLookup) bool
	// DevImage is the vanilla replacement.
	devImage string
	// TransformService rewrites the workload's main service to match
	// what the dev image expects. The lookup gives access to env_file
	// values so chart secrets can be re-emitted under the upstream
	// image's env names.
	transformService func(svc *compose.Service, lookup envLookup)
}

// envLookup unifies access to env values that may live on the
// service's Environment map (literal values + downward-API
// substitutions), in the .env file (values sourced from a k8s
// Secret via valueFrom: secretKeyRef), or in a materialized secret
// file referenced by a Bitnami-style `<NAME>_FILE` env var. Dev-swap
// rules read through this so they don't miss values just because
// a chart sourced them indirectly.
type envLookup struct {
	literal      map[string]string
	envFileLines *[]string
	envFileSeen  map[string]bool
	// configFiles is the materialized configs/+secrets/ map keyed
	// by relative path. When a chart env points at a file path
	// like /opt/bitnami/mongodb/secrets/mongodb-root-password
	// instead of carrying the value, we resolve it by matching
	// the basename against entries here.
	configFiles map[string]string
}

// get returns the env value under name and whether it was found.
// Searches the literal Environment first (it always wins because
// downward-API substitutions override env_file in k8s too), then
// .env entries for secret-derived values, and finally the
// `<NAME>_FILE` indirection (Bitnami's pattern of pointing env
// vars at a materialized secret file instead of carrying the
// value directly).
func (l envLookup) get(name string) (string, bool) {
	if v, ok := l.literal[name]; ok && v != "" {
		return v, true
	}
	if v := l.findInEnvFile(name); v != "" {
		return v, true
	}
	// _FILE indirection: e.g. MONGODB_ROOT_PASSWORD missing but
	// MONGODB_ROOT_PASSWORD_FILE = "/opt/bitnami/mongodb/secrets/mongodb-root-password".
	// The basename of that path is the Secret key; look it up in
	// the materialized files map.
	fileEnv := name + "_FILE"
	path, ok := l.literal[fileEnv]
	if !ok || path == "" {
		path = l.findInEnvFile(fileEnv)
	}
	if path != "" && l.configFiles != nil {
		base := basename(path)
		// Direct path lookup first (covers the case where the chart
		// env happens to match our materialized layout exactly).
		if v, ok := l.configFiles[strings.TrimPrefix(path, "/")]; ok {
			return v, true
		}
		// Fallback: match by basename across all materialized
		// secret files. The container's mount path may not line up
		// with our out-dir/secrets/<name>/<key> layout, but the
		// basename is preserved across both.
		for relPath, content := range l.configFiles {
			if !strings.HasPrefix(relPath, "secrets/") {
				continue
			}
			if basename(relPath) == base {
				return content, true
			}
		}
	}
	return "", false
}

// findInEnvFile searches the .env lines for a KEY="value" entry
// and returns the unquoted value. Returns "" when not present.
func (l envLookup) findInEnvFile(name string) string {
	if l.envFileLines == nil {
		return ""
	}
	prefix := name + "="
	for _, line := range *l.envFileLines {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		v := strings.TrimPrefix(line, prefix)
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		v = strings.ReplaceAll(v, `\"`, `"`)
		return v
	}
	return ""
}

// basename returns the last path segment of p (the part after the
// final `/`). Lets us match a container-side file path
// ("/opt/bitnami/mongodb/secrets/mongodb-root-password") against
// our host-side materialized file layout
// ("secrets/mongodb/mongodb-root-password") without needing to
// reverse-engineer the volume mount table.
func basename(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// addToEnvFile re-emits a translated key/value into the .env file
// so the swapped image picks it up via env_file. Existing entries
// are left untouched (matches addEnvFileLine's first-write-wins
// semantics).
func (l envLookup) addToEnvFile(name, value string) {
	if l.envFileLines == nil || value == "" {
		return
	}
	if l.envFileSeen[name] {
		return
	}
	l.envFileSeen[name] = true
	escaped := strings.ReplaceAll(value, `"`, `\"`)
	*l.envFileLines = append(*l.envFileLines, fmt.Sprintf(`%s="%s"`, name, escaped))
}

var devSwapRules = []devSwapRule{
	{
		name: "Bitnami MongoDB clustered chart → mongo:7 standalone",
		matchImage: func(img string) bool {
			return imageMatches(img, "bitnami/mongodb", "bitnamilegacy/mongodb")
		},
		matchEnv: func(l envLookup) bool {
			// Replica-set mode signals are the canonical cluster
			// bootstrap markers — they're set by the helm chart only
			// when running as a StatefulSet replica set.
			for _, k := range []string{
				"MONGODB_REPLICA_SET_MODE",
				"MONGODB_REPLICA_SET_NAME",
				"MONGODB_INITIAL_PRIMARY_HOST",
			} {
				if v, ok := l.get(k); ok && v != "" {
					return true
				}
			}
			return false
		},
		devImage:         "mongo:7",
		transformService: standaloneMongo,
	},
	{
		name: "Bitnami RabbitMQ clustered chart → rabbitmq:3-management standalone",
		matchImage: func(img string) bool {
			return imageMatches(img, "bitnami/rabbitmq", "bitnamilegacy/rabbitmq")
		},
		matchEnv: func(l envLookup) bool {
			// USE_LONGNAME=true together with an `@` in NODE_NAME is
			// the chart's clustering setup; USE_LONGNAME alone or a
			// short node name would be a non-clustered config.
			if v, _ := l.get("RABBITMQ_USE_LONGNAME"); v == "true" {
				return true
			}
			if v, _ := l.get("RABBITMQ_NODE_NAME"); strings.Contains(v, "@") {
				return true
			}
			return false
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
func applyDevSwap(
	svcName string,
	main *compose.Service,
	extras map[string]compose.Service,
	preserveImage bool,
	envFileLines *[]string,
	envFileSeen map[string]bool,
	configFiles map[string]string,
) string {
	if preserveImage {
		return ""
	}
	if main.Environment == nil {
		main.Environment = map[string]string{}
	}
	if envFileSeen == nil {
		envFileSeen = map[string]bool{}
	}
	lookup := envLookup{
		literal:      main.Environment,
		envFileLines: envFileLines,
		envFileSeen:  envFileSeen,
		configFiles:  configFiles,
	}
	for _, rule := range devSwapRules {
		if !rule.matchImage(main.Image) {
			continue
		}
		if !rule.matchEnv(lookup) {
			continue
		}
		original := main.Image
		main.Image = rule.devImage
		if rule.transformService != nil {
			rule.transformService(main, lookup)
		}
		// The k8s-FQDN hostname we set for cluster-mode workloads
		// (mongodb.mongodb-headless.default.svc.cluster.local etc.)
		// exists to make Erlang's USE_LONGNAME binding work for
		// rabbit and similar. Vanilla images don't need it, and
		// some (mongo:7's first-stage setup mongod) reject it
		// outright with "setup bind: Invalid argument" — clear it
		// and let docker assign the default short container hostname.
		// Network aliases still cover external lookups.
		main.Hostname = ""
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
//     reads on first boot. Values that came from a Secret land in
//     .env (env_file); the translated names are re-emitted there
//     too so mongo:7 sees them via env_file. Mongo:7 requires both
//     to be set together — if only the password is available the
//     username defaults to "root" (Bitnami's chart default).
//   - Drops every other MONGODB_*, MY_POD_*, K8S_*, BITNAMI_* env so
//     the vanilla image sees only what it understands. (Mongo:7 just
//     ignores unknown env, but a clean compose file is easier to
//     reason about.)
//   - Drops chart-specific volume mounts (configs/scripts directories
//     that the chart pre-populates and the standalone image has no
//     use for). The PVC-backed datadir survives.
//   - Clears command/entrypoint — the upstream image's defaults
//     (`mongod --bind_ip_all`) are exactly what we want.
func standaloneMongo(svc *compose.Service, lookup envLookup) {
	user, _ := lookup.get("MONGODB_ROOT_USER")
	pass, hasPass := lookup.get("MONGODB_ROOT_PASSWORD")
	// Bitnami defaults the root user to "root" when only the
	// password is set; mongo:7 errors out if either is empty, so
	// fill in the default to keep the connection-string contract.
	if user == "" && hasPass {
		user = "root"
	}
	if user != "" {
		svc.Environment["MONGO_INITDB_ROOT_USERNAME"] = user
		lookup.addToEnvFile("MONGO_INITDB_ROOT_USERNAME", user)
	}
	if pass != "" {
		svc.Environment["MONGO_INITDB_ROOT_PASSWORD"] = pass
		lookup.addToEnvFile("MONGO_INITDB_ROOT_PASSWORD", pass)
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
// reads). Values sourced from a Secret arrive via env_file; we
// re-emit the translated names there too. Cluster/node-name env
// vars are dropped so the Erlang VM uses its default short-name
// binding.
func standaloneRabbit(svc *compose.Service, lookup envLookup) {
	user, _ := lookup.get("RABBITMQ_USERNAME")
	pass, _ := lookup.get("RABBITMQ_PASSWORD")
	if user != "" {
		svc.Environment["RABBITMQ_DEFAULT_USER"] = user
		lookup.addToEnvFile("RABBITMQ_DEFAULT_USER", user)
	}
	if pass != "" {
		svc.Environment["RABBITMQ_DEFAULT_PASS"] = pass
		lookup.addToEnvFile("RABBITMQ_DEFAULT_PASS", pass)
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
