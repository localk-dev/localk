// Package kubectl shells out to the kubectl binary for cluster ingestion.
//
// Safety property: the only kubectl invocations this package ever produces
// are read-only — `kubectl get`, `kubectl config view`, and
// `kubectl config current-context`. The Default runner enforces this
// allowlist before spawning the process. Any other verb returns an error.
// This is defense-in-depth: callers in this package only construct safe
// arguments, but the runner-level check guards against future regressions
// that could accidentally widen the surface.
package kubectl

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner abstracts the act of running kubectl so tests can inject a fake.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// FetchOptions configures a Fetch call.
type FetchOptions struct {
	Namespace string // required
	Context   string // optional; defaults to the current kubeconfig context
}

// Default returns a Runner that shells out to the real kubectl binary, but
// only after validating the requested verb against ReadOnlyVerbs. Calls
// outside the allowlist return ErrDisallowedVerb without spawning a process.
func Default() Runner {
	return defaultRunner{}
}

// ErrDisallowedVerb is returned by Default's Runner when asked to invoke a
// kubectl subcommand that is not on the read-only allowlist.
var ErrDisallowedVerb = errors.New("kubectl: disallowed verb (this binary only invokes read-only commands)")

// ErrKubectlMissing is returned when kubectl is not on PATH.
var ErrKubectlMissing = errors.New("kubectl: not found on PATH (install: https://kubernetes.io/docs/tasks/tools/)")

// readOnlyVerbs is the hard-coded allowlist enforced by the Default runner.
// Adding to this set is the only way to invoke a new kubectl subcommand from
// localk, and any addition should be reviewed for read-only semantics.
var readOnlyVerbs = map[string]bool{
	"get":    true,
	"config": true, // further restricted below to view + current-context
}

var readOnlyConfigSubverbs = map[string]bool{
	"view":            true,
	"current-context": true,
}

type defaultRunner struct{}

func (defaultRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%w: no verb provided", ErrDisallowedVerb)
	}
	verb := args[0]
	if !readOnlyVerbs[verb] {
		return nil, fmt.Errorf("%w: %q", ErrDisallowedVerb, verb)
	}
	if verb == "config" {
		if len(args) < 2 || !readOnlyConfigSubverbs[args[1]] {
			sub := ""
			if len(args) >= 2 {
				sub = args[1]
			}
			return nil, fmt.Errorf("%w: config %q", ErrDisallowedVerb, sub)
		}
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, fmt.Errorf("kubectl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return out, fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// Available reports whether kubectl is present on PATH.
func Available(_ Runner) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return ErrKubectlMissing
	}
	return nil
}

// CurrentContext returns the active kubeconfig context.
func CurrentContext(r Runner) (string, error) {
	out, err := r.Run(context.Background(), "config", "current-context")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentNamespace returns the namespace of the active kubeconfig context.
// Falls back to "default" if none is set.
func CurrentNamespace(r Runner) (string, error) {
	out, err := r.Run(context.Background(), "config", "view", "--minify", "-o", "jsonpath={..namespace}")
	if err != nil {
		return "", err
	}
	ns := strings.TrimSpace(string(out))
	if ns == "" {
		return "default", nil
	}
	return ns, nil
}

// Fetch retrieves the resource kinds localk understands from a single
// namespace, returning the raw YAML bytes (a single `kind: List` document).
//
// The kinds requested mirror what internal/convert can translate today:
// Deployment, StatefulSet, Service, ConfigMap, Secret,
// PersistentVolumeClaim. Adding a new kind here without converter support
// is harmless (it'll be ignored by the parser) but pointless.
func Fetch(r Runner, ctx context.Context, opts FetchOptions) ([]byte, error) {
	if opts.Namespace == "" {
		return nil, fmt.Errorf("kubectl Fetch: namespace is required")
	}
	args := []string{
		"get",
		"deployment,statefulset,service,configmap,secret,persistentvolumeclaim",
		"-n", opts.Namespace,
		"-o", "yaml",
	}
	if opts.Context != "" {
		args = append(args, "--context", opts.Context)
	}
	return r.Run(ctx, args...)
}
