package kubectl

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeRunner records the args it was called with and returns canned output.
// It does NOT enforce the verb allowlist — that's the Default runner's job
// and is tested separately against the real implementation.
type fakeRunner struct {
	output []byte
	err    error
	got    [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.got = append(f.got, append([]string(nil), args...))
	return f.output, f.err
}

func TestCurrentContext(t *testing.T) {
	r := &fakeRunner{output: []byte("prod-eu1\n")}
	ctx, err := CurrentContext(r)
	if err != nil {
		t.Fatalf("CurrentContext: %v", err)
	}
	if ctx != "prod-eu1" {
		t.Errorf("got %q, want %q", ctx, "prod-eu1")
	}
	if len(r.got) != 1 || !reflect.DeepEqual(r.got[0], []string{"config", "current-context"}) {
		t.Errorf("unexpected args: %v", r.got)
	}
}

func TestCurrentNamespace(t *testing.T) {
	r := &fakeRunner{output: []byte("my-namespace\n")}
	ns, err := CurrentNamespace(r)
	if err != nil {
		t.Fatalf("CurrentNamespace: %v", err)
	}
	if ns != "my-namespace" {
		t.Errorf("got %q, want %q", ns, "my-namespace")
	}
	if len(r.got) != 1 || !reflect.DeepEqual(r.got[0], []string{"config", "view", "--minify", "-o", "jsonpath={..namespace}"}) {
		t.Errorf("unexpected args: %v", r.got)
	}
}

func TestCurrentNamespace_Empty(t *testing.T) {
	r := &fakeRunner{output: []byte("")}
	ns, err := CurrentNamespace(r)
	if err != nil {
		t.Fatalf("CurrentNamespace: %v", err)
	}
	if ns != "default" {
		t.Errorf("expected fallback to %q, got %q", "default", ns)
	}
}

func TestFetch_BuildsArgs(t *testing.T) {
	r := &fakeRunner{output: []byte("kind: List\nitems: []\n")}
	out, err := Fetch(r, context.Background(), FetchOptions{Namespace: "ns1"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(out) != "kind: List\nitems: []\n" {
		t.Errorf("unexpected output: %q", out)
	}
	want := []string{"get", "deployment,statefulset,service,configmap,secret,persistentvolumeclaim,ingress", "-n", "ns1", "-o", "yaml"}
	if !reflect.DeepEqual(r.got[0], want) {
		t.Errorf("args mismatch:\n got  %v\n want %v", r.got[0], want)
	}
}

func TestFetch_WithContext(t *testing.T) {
	r := &fakeRunner{}
	if _, err := Fetch(r, context.Background(), FetchOptions{Namespace: "ns1", Context: "prod"}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := []string{"get", "deployment,statefulset,service,configmap,secret,persistentvolumeclaim,ingress", "-n", "ns1", "-o", "yaml", "--context", "prod"}
	if !reflect.DeepEqual(r.got[0], want) {
		t.Errorf("args mismatch:\n got  %v\n want %v", r.got[0], want)
	}
}

func TestFetch_NamespaceRequired(t *testing.T) {
	r := &fakeRunner{}
	if _, err := Fetch(r, context.Background(), FetchOptions{}); err == nil {
		t.Fatal("expected error for missing namespace")
	}
	if len(r.got) != 0 {
		t.Errorf("Fetch should not have invoked the runner: %v", r.got)
	}
}

// TestDefaultRunner_RejectsDisallowedVerbs is the core safety test. It pokes
// the real Default runner with each of the destructive kubectl verbs and
// asserts that the runner refuses before spawning a process. If this test
// ever fails, localk has lost its read-only guarantee.
func TestDefaultRunner_RejectsDisallowedVerbs(t *testing.T) {
	r := Default()
	denied := []string{
		"apply",
		"delete",
		"patch",
		"edit",
		"create",
		"replace",
		"exec",
		"cp",
		"port-forward",
		"proxy",
		"run",
		"scale",
		"rollout",
		"drain",
		"cordon",
		"uncordon",
		"taint",
		"label",
		"annotate",
	}
	for _, verb := range denied {
		t.Run(verb, func(t *testing.T) {
			_, err := r.Run(context.Background(), verb, "anything")
			if err == nil {
				t.Fatalf("Default runner accepted disallowed verb %q", verb)
			}
			if !errors.Is(err, ErrDisallowedVerb) {
				t.Fatalf("expected ErrDisallowedVerb, got %v", err)
			}
		})
	}
}

// TestDefaultRunner_RejectsConfigSubverbs ensures the `config` verb is
// further constrained — only `view` and `current-context` are read-only,
// and the runner must reject anything else (e.g. `set-context`,
// `delete-context`, `rename-context`, `unset`, `use-context`).
func TestDefaultRunner_RejectsConfigSubverbs(t *testing.T) {
	r := Default()
	denied := []string{"set-context", "delete-context", "rename-context", "unset", "use-context", "set"}
	for _, sub := range denied {
		t.Run(sub, func(t *testing.T) {
			_, err := r.Run(context.Background(), "config", sub)
			if err == nil {
				t.Fatalf("Default runner accepted disallowed config subverb %q", sub)
			}
			if !errors.Is(err, ErrDisallowedVerb) {
				t.Fatalf("expected ErrDisallowedVerb, got %v", err)
			}
		})
	}
}

// TestDefaultRunner_NoArgs verifies the empty-args edge case.
func TestDefaultRunner_NoArgs(t *testing.T) {
	r := Default()
	_, err := r.Run(context.Background())
	if err == nil || !errors.Is(err, ErrDisallowedVerb) {
		t.Fatalf("expected ErrDisallowedVerb for empty args, got %v", err)
	}
}
