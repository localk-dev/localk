package convert

import (
	"fmt"
	"sort"
	"strings"

	"github.com/localk-dev/localk/internal/compose"
	"github.com/localk-dev/localk/internal/kube"
)

// proxyServiceName is the compose service we add to host the Caddy reverse
// proxy. The name is deliberately short so it doesn't clash with anything
// users typically have in their k8s manifests.
const proxyServiceName = "proxy"

// route is one resolved Ingress path → backend mapping, as the Caddyfile
// renderer wants it: host comes from the grouping key, everything else is
// here.
type route struct {
	path     string // includes leading slash; "" or "/" means catch-all
	pathType string
	backend  string
	port     int32
}

// buildProxy builds the Caddy reverse-proxy compose service plus the
// Caddyfile that drives it, from the parsed Ingress resources. Returns
// (nil, "", nil, warnings) when there are no Ingresses — the caller skips
// proxy emission entirely in that case.
//
// The set of backend service names is also returned so the caller can
// strip host-port publishing from those services. Without that, multiple
// services binding host:80 collide and `docker compose up` fails on the
// second one. With it, only the proxy publishes :80 and traffic reaches
// the backends through the compose network.
func buildProxy(
	ingresses []kube.Ingress,
	services map[string]compose.Service,
) (svc *compose.Service, caddyfile string, backendNames map[string]bool, warnings []string) {
	if len(ingresses) == 0 {
		return nil, "", nil, nil
	}

	backendNames = map[string]bool{}

	// Group routes by host. Caddy emits one site block per host, with
	// path handlers inside.
	byHost := map[string][]route{}

	for _, ing := range ingresses {
		if isEphemeralIngress(ing) {
			// cert-manager HTTP-01 solvers and similar ephemeral
			// infrastructure ingresses don't belong in a local dev stack —
			// they exist for ~1 minute during cert renewal and reference
			// services that disappear with them. Skip silently.
			continue
		}
		for _, rule := range ing.Spec.Rules {
			host := rule.Host
			if host == "" {
				warnings = append(warnings, fmt.Sprintf(
					"ingress %q has a rule with no host; rules without a host can't be routed locally and will be skipped",
					ing.Metadata.Name,
				))
				continue
			}
			if rule.HTTP == nil || len(rule.HTTP.Paths) == 0 {
				warnings = append(warnings, fmt.Sprintf(
					"ingress %q rule for host %q has no HTTP paths; skipping",
					ing.Metadata.Name, host,
				))
				continue
			}
			for _, p := range rule.HTTP.Paths {
				port, ok := resolveBackendPort(p.Backend, services)
				if !ok {
					warnings = append(warnings, fmt.Sprintf(
						"ingress %q routes %s%s to service %q, but no compose service with that name exists; skipping this rule",
						ing.Metadata.Name, host, p.Path, p.Backend.Service.Name,
					))
					continue
				}
				byHost[host] = append(byHost[host], route{
					path:     p.Path,
					pathType: p.PathType,
					backend:  p.Backend.Service.Name,
					port:     port,
				})
				backendNames[p.Backend.Service.Name] = true
			}
		}
	}

	if len(byHost) == 0 {
		// All rules were skipped — don't emit a useless proxy service.
		return nil, "", backendNames, warnings
	}

	caddyfile = renderCaddyfile(byHost)

	proxy := compose.Service{
		Image:   "caddy:2-alpine",
		Restart: "unless-stopped",
		Ports:   []string{"80:80"},
		Volumes: []any{"./Caddyfile:/etc/caddy/Caddyfile:ro"},
	}
	return &proxy, caddyfile, backendNames, warnings
}

// resolveBackendPort returns the port to forward to for an Ingress backend.
// It prefers an explicit port number, falls back to looking up a named
// port in the target compose service's port mappings, and finally falls
// back to 80 (which is what most k8s services expose internally).
func resolveBackendPort(b kube.IngressBackend, services map[string]compose.Service) (int32, bool) {
	target, ok := services[b.Service.Name]
	if !ok {
		return 0, false
	}
	if b.Service.Port.Number > 0 {
		return b.Service.Port.Number, true
	}
	// We don't have rich port metadata in compose.Service.Ports — they're
	// already string-formatted. For the named-port case we can't reliably
	// look up which container port a given name maps to, so default to 80
	// and warn the caller via the broader warnings system if needed. In
	// practice the most common shape is `port: { number: 80 }` so this
	// path is rare.
	if len(target.Ports) > 0 {
		// Try to parse the container side of the first published port
		// (`80:80` → 80, `3000:8080` → 8080).
		first := target.Ports[0]
		if i := strings.LastIndex(first, ":"); i >= 0 {
			var n int32
			if _, err := fmt.Sscanf(first[i+1:], "%d", &n); err == nil && n > 0 {
				return n, true
			}
		}
	}
	return 80, true
}

// renderCaddyfile produces a Caddyfile string from the host→routes map.
// Hosts and paths are sorted so output is deterministic across runs.
func renderCaddyfile(byHost map[string][]route) string {
	var b strings.Builder

	// Global options block: disable HTTPS auto-issuance (not relevant
	// locally) and the admin API (no remote management of a local proxy).
	b.WriteString("{\n\tauto_https off\n\tadmin off\n}\n\n")

	hosts := make([]string, 0, len(byHost))
	for h := range byHost {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)

	for _, host := range hosts {
		routes := byHost[host]
		// Stable order: catch-all rules last so handle_path entries take
		// precedence; among non-catch-all, sort by path so the output is
		// deterministic.
		sort.SliceStable(routes, func(i, j int) bool {
			ai, aj := isCatchAll(routes[i]), isCatchAll(routes[j])
			if ai != aj {
				return !ai // non-catch-all first
			}
			return routes[i].path < routes[j].path
		})

		// Track paths we've already emitted under this host so duplicates
		// (or rules that resolve to the same path) become a warning rather
		// than a Caddy error at startup. We don't surface that warning
		// from inside renderCaddyfile — it's a pure formatter — so the
		// dedup happens silently here. The grouping caller already warned
		// about missing backends.
		seenPaths := map[string]bool{}

		fmt.Fprintf(&b, "%s {\n", localHostFor(host))
		for _, r := range routes {
			if seenPaths[r.path] {
				continue
			}
			seenPaths[r.path] = true
			writeRoute(&b, r)
		}
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func isCatchAll(r route) bool {
	return r.path == "" || r.path == "/"
}

func writeRoute(b *strings.Builder, r route) {
	upstream := fmt.Sprintf("%s:%d", r.backend, r.port)
	if isCatchAll(r) {
		// No path → forward everything for this host.
		fmt.Fprintf(b, "\treverse_proxy %s\n", upstream)
		return
	}
	switch strings.ToLower(r.pathType) {
	case "exact":
		// Exact match — Caddy's `handle` with the literal path matcher.
		fmt.Fprintf(b, "\thandle %s {\n\t\treverse_proxy %s\n\t}\n", r.path, upstream)
	default:
		// Prefix (or unset / ImplementationSpecific). handle_path strips
		// the prefix before forwarding, matching the behavior most apps
		// expect when they're mounted under a sub-path.
		prefix := strings.TrimRight(r.path, "/")
		fmt.Fprintf(b, "\thandle_path %s/* {\n\t\treverse_proxy %s\n\t}\n", prefix, upstream)
	}
}

// localHostFor maps a production hostname to the local equivalent by
// replacing the last domain segment with `localhost`. `*.localhost`
// resolves to 127.0.0.1 automatically on macOS, Linux, and Windows, so
// this avoids /etc/hosts edits while preserving the subdomain hierarchy
// that distinguishes services in prod.
//
//	example.com           → example.localhost
//	api.example.com       → api.example.localhost
//	logs.app.example.com  → logs.app.example.localhost
//	single                → single.localhost
func localHostFor(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return host
	}
	// If the host already ends in .localhost, leave it alone.
	if strings.HasSuffix(host, ".localhost") || host == "localhost" {
		return host
	}
	if i := strings.LastIndex(host, "."); i >= 0 {
		return host[:i] + ".localhost"
	}
	return host + ".localhost"
}

// isEphemeralIngress recognizes Ingresses that exist only as part of
// cluster infrastructure (cert-manager HTTP-01 solvers being the
// near-universal example) and shouldn't appear in a local dev stack.
//
// We match on three signals so the filter survives different
// cert-manager versions and label-vs-annotation conventions:
//
//   - the standard cert-manager label/annotation
//     `acme.cert-manager.io/http01-solver: "true"`
//   - the "cm-acme-http-solver-" name prefix that cert-manager always
//     uses for these resources
//   - any rule whose path starts with `/.well-known/acme-challenge/`
//     (Let's Encrypt's protocol-level marker — only solvers use it)
//
// Any one match is enough. Three independent signals means a
// cert-manager rename or label drift in a future version still gets
// caught.
func isEphemeralIngress(ing kube.Ingress) bool {
	if ing.Metadata.Annotations["acme.cert-manager.io/http01-solver"] == "true" {
		return true
	}
	if ing.Metadata.Labels["acme.cert-manager.io/http01-solver"] == "true" {
		return true
	}
	if strings.HasPrefix(ing.Metadata.Name, "cm-acme-http-solver-") {
		return true
	}
	for _, rule := range ing.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, p := range rule.HTTP.Paths {
			if strings.HasPrefix(p.Path, "/.well-known/acme-challenge/") {
				return true
			}
		}
	}
	return false
}

// resolveHostPortConflicts walks every service's Ports and drops any
// host-port mapping that's already been claimed by an earlier service.
// Two scenarios surface this:
//
//  1. An Ingress-driven stack: Caddy proxy publishes :80, and some
//     non-routed services in the manifest also declare host:80.
//     Without resolution, `docker compose up` fails on the second
//     bind.
//  2. A no-Ingress stack where multiple services genuinely declare
//     the same host port (rare, but k8s tolerates it because
//     NodePort uses different host ports — compose doesn't have an
//     equivalent abstraction).
//
// Resolution: first-claim wins. Any service named "proxy" (the Caddy
// service we emit) is processed first so it always keeps its bind.
// Dropped mappings produce a warning naming the service so the user
// knows what went where. Affected services remain reachable through
// the compose network for service-to-service traffic.
func resolveHostPortConflicts(services map[string]compose.Service) []string {
	var warnings []string
	claimed := map[string]string{}

	// Proxy first — its publish is what makes Ingress work; everything
	// else can lose theirs.
	if proxy, ok := services["proxy"]; ok {
		for _, p := range proxy.Ports {
			if h := hostPortPrefix(p); h != "" {
				claimed[h] = "proxy"
			}
		}
	}

	// Iterate other services in sorted order so the warning text is
	// deterministic across runs.
	names := make([]string, 0, len(services))
	for name := range services {
		if name != "proxy" {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		s := services[name]
		var kept []string
		var dropped []string
		for _, p := range s.Ports {
			h := hostPortPrefix(p)
			if h == "" {
				kept = append(kept, p)
				continue
			}
			if claimer, taken := claimed[h]; taken {
				dropped = append(dropped, fmt.Sprintf("%s (claimed by %q)", p, claimer))
				continue
			}
			claimed[h] = name
			kept = append(kept, p)
		}
		if len(dropped) > 0 {
			s.Ports = kept
			services[name] = s
			warnings = append(warnings, fmt.Sprintf(
				"service %q: dropped host-port mapping(s) %v — reachable via the compose network only. To publish on a different host port, override in localk.yaml or use `localk dev` to swap it for a local process.",
				name, dropped,
			))
		}
	}
	return warnings
}

// hostPortPrefix extracts the host-side port from a compose Ports
// string ("80:80", "8080:80", "127.0.0.1:80:80/tcp") for conflict
// detection. Returns "" when the entry has no explicit host port
// (e.g. just ":80" — uncommon but possible) since those don't
// participate in conflicts.
func hostPortPrefix(p string) string {
	// Trim "/tcp", "/udp" suffixes.
	if i := strings.Index(p, "/"); i >= 0 {
		p = p[:i]
	}
	parts := strings.Split(p, ":")
	switch len(parts) {
	case 2:
		// "host:container" — the first part is the host port.
		return parts[0]
	case 3:
		// "ip:host:container" — middle is the host port. Include the
		// IP in the key so 127.0.0.1:80 and 0.0.0.0:80 don't appear
		// to conflict (they technically do at OS level on the same
		// interface, but compose treats explicit-IP binds as a
		// distinct surface).
		return parts[0] + ":" + parts[1]
	}
	return ""
}

// stripBackendPorts clears the Ports field for each compose service that
// is referenced as an Ingress backend. After this, only the proxy
// publishes :80 to the host; backends are reachable via the proxy and
// via intra-compose DNS for service-to-service traffic.
//
// This is the second half of the Ingress fix — without it, multiple
// services binding host:80 (very common in k8s where containers serve
// on :80 internally) cause `docker compose up` to fail on the second
// service.
func stripBackendPorts(services map[string]compose.Service, backends map[string]bool) {
	for name := range backends {
		s, ok := services[name]
		if !ok {
			continue
		}
		s.Ports = nil
		services[name] = s
	}
}
