package convert

import "strings"

// resolveFieldRef maps a Kubernetes downward-API fieldPath to a sensible
// local equivalent. k8s injects pod metadata into env vars at startup
// time (`metadata.name`, `status.podIP`, etc); locally there's only one
// "pod" per service so we substitute deterministic values that keep
// dependent apps working without surprising them.
//
// Returns ("", false) for paths we don't recognize. Callers should warn.
func resolveFieldRef(fieldPath string, w workload) (string, bool) {
	switch fieldPath {
	case "metadata.name":
		// k8s pod names are `<sts>-<ordinal>` for StatefulSets and
		// `<deploy>-<replicaset>-<rand>` for Deployments. Bitnami
		// charts use MY_POD_NAME to compare against
		// MONGODB_INITIAL_PRIMARY_HOST etc., which use the StatefulSet
		// pod-0 form — so for StatefulSets we MUST substitute the
		// ordinal-zero name (mongodb-0, not mongodb), or the chart
		// thinks it's a secondary, never accepts writes, and the data
		// dir-empty bootstrap loop never settles. Compose runs a
		// single container so ordinal 0 is the correct identity.
		if w.kindLabel == "statefulset" {
			return w.name + "-0", true
		}
		return w.name, true
	case "metadata.namespace":
		// Compose has no namespaces. "default" is the universal fallback
		// most apps tolerate.
		return "default", true
	case "metadata.uid":
		// Stable, unique-ish per workload — fine for apps that just need
		// a non-empty identifier.
		return w.name + "-local", true
	case "status.podIP", "status.hostIP":
		// On the compose network, services reach each other by name. An
		// app that wants its own IP usually does so to advertise itself
		// to peers, which works fine if it advertises its service name.
		return w.name, true
	case "spec.nodeName":
		return "docker-host", true
	case "spec.serviceAccountName":
		return "default", true
	}
	return "", false
}

// expandRefs replaces every $(VAR) occurrence in s with env[VAR], leaving
// the original literal in place when VAR is unknown. Mirrors kubelet's
// env-var expansion in pod containers.
//
// Notes on the dialect we implement:
//
//   - We support `$(VAR_NAME)` only. The `${VAR}` and bare `$VAR` shell
//     forms are not part of the Kubernetes downward-API expansion.
//   - `$$` is the documented escape for a literal `$`. We honor it.
//   - Unknown vars are left as `$(NAME)` literally (matching k8s) so the
//     downstream container at least sees the original text rather than
//     an empty string masquerading as a value.
func expandRefs(s string, env map[string]string) string {
	if !strings.ContainsAny(s, "$") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		// `$$` → literal `$`
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		// `$(NAME)` → env[NAME] if known, else passthrough.
		if i+1 < len(s) && s[i+1] == '(' {
			if end := strings.IndexByte(s[i+2:], ')'); end >= 0 {
				name := s[i+2 : i+2+end]
				if v, ok := env[name]; ok {
					b.WriteString(v)
				} else {
					// Unknown — keep the literal $(NAME) so the user can
					// see what was intended.
					b.WriteString(s[i : i+3+end])
				}
				i += 2 + end
				continue
			}
		}
		// Anything else: bare `$` not followed by `$` or `(` — write through.
		b.WriteByte('$')
	}
	return b.String()
}
