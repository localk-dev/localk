package devmode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverlay_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.dev.yml")

	// Empty load: missing file is fine.
	o, exists, err := Load(path)
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	if exists {
		t.Errorf("Load reported file exists when it shouldn't")
	}
	if len(o.Services) != 0 {
		t.Errorf("expected empty Services, got %v", o.Services)
	}

	// Add two proxies.
	o.AddProxy("api", 80, 3000)
	o.AddProxy("worker", 8080, 3001)
	if err := o.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload — expect both services to round-trip with correct ports.
	o2, exists, err := Load(path)
	if err != nil {
		t.Fatalf("Load(after save): %v", err)
	}
	if !exists {
		t.Errorf("expected file to exist after Save")
	}
	if len(o2.Services) != 2 {
		t.Fatalf("expected 2 services after reload, got %d", len(o2.Services))
	}
	api, ok := o2.Services["api"]
	if !ok {
		t.Fatal("api missing after round-trip")
	}
	wantEntrypoint := []string{"socat", "-d", "TCP-LISTEN:80,fork,reuseaddr", "TCP:host.docker.internal:3000"}
	if !equalStrings(api.Entrypoint, wantEntrypoint) {
		t.Errorf("api.Entrypoint = %v, want %v", api.Entrypoint, wantEntrypoint)
	}

	// Remove one — file should still exist with the remaining service.
	if !o2.RemoveProxy("api") {
		t.Errorf("RemoveProxy(api) returned false; expected true")
	}
	if err := o2.Save(path); err != nil {
		t.Fatalf("Save after remove: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file should still exist after removing one of two services: %v", err)
	}

	// Remove the last service — file should be deleted to keep the
	// working directory tidy.
	o3, _, _ := Load(path)
	o3.RemoveProxy("worker")
	if err := o3.Save(path); err != nil {
		t.Fatalf("Save after last remove: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be removed when overlay is empty, got err: %v", err)
	}
}

func TestOverlay_RemoveMissingReturnsFalse(t *testing.T) {
	o := &Overlay{Services: map[string]ProxyService{}}
	if o.RemoveProxy("not-there") {
		t.Error("RemoveProxy on missing service should return false")
	}
}

func TestOverlay_ProxyNamesSorted(t *testing.T) {
	o := &Overlay{Services: map[string]ProxyService{}}
	o.AddProxy("zebra", 80, 3000)
	o.AddProxy("alpha", 80, 3001)
	o.AddProxy("middle", 80, 3002)
	got := o.ProxyNames()
	want := []string{"alpha", "middle", "zebra"}
	if !equalStrings(got, want) {
		t.Errorf("ProxyNames = %v, want %v", got, want)
	}
}

func TestContainerPortFor(t *testing.T) {
	cases := []struct {
		name  string
		ports []string
		def   int
		want  int
	}{
		{"empty falls back to default", nil, 80, 80},
		{"standard host:container", []string{"5432:5432"}, 80, 5432},
		{"different host and container", []string{"8080:3000"}, 80, 3000},
		{"with protocol suffix", []string{"5432:5432/tcp"}, 80, 5432},
		{"unparseable falls back", []string{"not-a-port"}, 80, 80},
		{"first wins", []string{"5432:5432", "8080:8080"}, 80, 5432},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContainerPortFor(tc.ports, tc.def); got != tc.want {
				t.Errorf("ContainerPortFor(%v, %d) = %d, want %d", tc.ports, tc.def, got, tc.want)
			}
		})
	}
}

func TestHostPortsFromBase(t *testing.T) {
	in := map[string][]string{
		"postgres": {"5432:5432"},
		"redis":    {"6379:6379"},
		"api":      {}, // behind ingress, no host port
		"weird":    {"8080:8080", "9090:9090"},
	}
	got := HostPortsFromBase(in)
	want := []HostPort{
		{"postgres", 5432},
		{"redis", 6379},
		{"weird", 8080},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for i, hp := range want {
		if got[i] != hp {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], hp)
		}
	}
}

func TestRewriteEnvForHost(t *testing.T) {
	ports := map[string][]string{
		"postgres":          {"5432:5432"},
		"redis":             {"6379:6379"},
		"messagebus":        {"4222:4222"},
		"renamed-host-port": {"8081:8080"}, // host 8081 → container 8080
	}
	in := `# header preserved
DATABASE_URL="postgres://postgres:5432/app"
REDIS_URL="redis://redis:6379"
NATS_URL="nats://messagebus:4222"
RENAMED="http://renamed-host-port:8080/api"
INTERNAL_ONLY="http://api:80/health"
PLAIN_TEXT="hello world"

EMPTY_LINE_ABOVE=preserved
`
	got := RewriteEnvForHost(in, ports)

	for _, want := range []string{
		"DATABASE_URL=\"postgres://localhost:5432/app\"",
		"REDIS_URL=\"redis://localhost:6379\"",
		"NATS_URL=\"nats://localhost:4222\"",
		"RENAMED=\"http://localhost:8081/api\"", // container 8080 → host 8081
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output, got:\n%s", want, got)
		}
	}

	// Services with no published port should NOT be rewritten — they're
	// only reachable inside the compose network.
	if !strings.Contains(got, "INTERNAL_ONLY=\"http://api:80/health\"") {
		t.Errorf("api with no published port should pass through unchanged:\n%s", got)
	}

	// Header / comment / blank line / non-host strings preserved.
	for _, must := range []string{"# header preserved", "PLAIN_TEXT=\"hello world\"", "EMPTY_LINE_ABOVE=preserved"} {
		if !strings.Contains(got, must) {
			t.Errorf("non-rewriteable line lost: %q\n%s", must, got)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
