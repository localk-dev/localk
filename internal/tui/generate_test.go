package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeRunner is a stand-in for kubectl.Default() so tests can drive
// the cluster path without a real kubeconfig. Stages canned outputs
// keyed by the first arg (verb) so a sequence of List calls can each
// get a different response.
type fakeKubectl struct {
	contexts   []byte
	namespaces []byte
	err        error
	calls      [][]string
}

func (f *fakeKubectl) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.err != nil {
		return nil, f.err
	}
	// Switch on the verb + args to return the right canned output.
	if len(args) >= 1 && args[0] == "config" {
		return f.contexts, nil
	}
	if len(args) >= 2 && args[0] == "get" && args[1] == "namespaces" {
		return f.namespaces, nil
	}
	return nil, nil
}

// pressKey is a small helper that crafts a tea.KeyMsg matching what
// the wizard expects from the Bubble Tea runtime. Wraps the messy
// rune-vs-named-key dance into one call site.
func pressKey(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// typePath simulates the user typing a path into the focused
// textinput. textinput's Update consumes one rune-key at a time.
func typePath(g generateModel, path string) generateModel {
	for _, r := range path {
		g, _ = g.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return g
}

func TestGenerate_FileBranch_HappyPath(t *testing.T) {
	dir := t.TempDir()
	g := newGenerateModel(dir)

	// Step 1: source picker — top item is "from file"; press Enter.
	g, _ = g.Update(pressKey("enter"))
	if g.step != genFilePath {
		t.Fatalf("after source enter: step = %v, want genFilePath", g.step)
	}
	if g.source != genSourceFile {
		t.Errorf("source = %v, want genSourceFile", g.source)
	}

	// Step 2: type a real path (the temp dir itself works as a directory).
	g = typePath(g, dir)
	g, _ = g.Update(pressKey("enter"))
	if g.step != genOutDir {
		t.Fatalf("after file path enter: step = %v, want genOutDir", g.step)
	}

	// Step 3: out-dir is pre-filled from newGenerateModel(outDir);
	// just press Enter.
	g, _ = g.Update(pressKey("enter"))
	if g.step != genConfirm {
		t.Fatalf("after out-dir enter: step = %v, want genConfirm", g.step)
	}

	// Step 4: confirm. Should populate dispatchAction with the
	// localk subprocess.
	g, _ = g.Update(pressKey("enter"))
	if g.dispatchAction == nil {
		t.Fatalf("after confirm: dispatchAction is nil; flash=%q", g.flash)
	}
	if !strings.Contains(g.dispatchCommand, "generate") {
		t.Errorf("dispatchCommand should mention generate, got %q", g.dispatchCommand)
	}
	if !strings.Contains(g.dispatchCommand, "--out-dir") {
		t.Errorf("dispatchCommand should pass --out-dir, got %q", g.dispatchCommand)
	}
}

func TestGenerate_FileBranch_RejectsMissingPath(t *testing.T) {
	dir := t.TempDir()
	g := newGenerateModel(dir)

	g, _ = g.Update(pressKey("enter")) // pick "from file"
	g = typePath(g, filepath.Join(dir, "does-not-exist"))
	g, _ = g.Update(pressKey("enter"))

	if g.step != genFilePath {
		t.Errorf("missing path should keep us on genFilePath, got %v", g.step)
	}
	if g.flash == "" {
		t.Errorf("expected validation flash for missing path")
	}
}

func TestGenerate_EscBacksUp(t *testing.T) {
	dir := t.TempDir()
	g := newGenerateModel(dir)

	// Walk forward two steps then back two — should land on genSource.
	g, _ = g.Update(pressKey("enter")) // genSource → genFilePath
	g = typePath(g, dir)
	g, _ = g.Update(pressKey("enter")) // genFilePath → genOutDir
	if g.step != genOutDir {
		t.Fatalf("setup: step = %v, want genOutDir", g.step)
	}
	g, _ = g.Update(pressKey("esc")) // → genFilePath
	if g.step != genFilePath {
		t.Errorf("esc from genOutDir: step = %v, want genFilePath", g.step)
	}
	g, _ = g.Update(pressKey("esc")) // → genSource
	if g.step != genSource {
		t.Errorf("esc from genFilePath: step = %v, want genSource", g.step)
	}
	g, _ = g.Update(pressKey("esc")) // → request exit (from genSource)
	if !g.requestExit {
		t.Errorf("esc from genSource: expected requestExit=true")
	}
}

func TestGenerate_ClusterBranch_HappyPath(t *testing.T) {
	dir := t.TempDir()
	g := newGenerateModel(dir)
	g.kubectlRunner = &fakeKubectl{
		contexts:   []byte("prod-eu1 staging dev-local"),
		namespaces: []byte("default kube-system my-ns"),
	}

	// Source: select item 1 (cluster).
	g, _ = g.Update(pressKey("down"))
	g, _ = g.Update(pressKey("enter"))
	if g.step != genContext {
		t.Fatalf("after source enter: step = %v, want genContext", g.step)
	}
	if len(g.contexts) != 3 || g.contexts[0] != "prod-eu1" {
		t.Fatalf("contexts not populated correctly: %v", g.contexts)
	}

	// Pick second context.
	g, _ = g.Update(pressKey("down"))
	g, _ = g.Update(pressKey("enter"))
	if g.context != "staging" {
		t.Errorf("context = %q, want staging", g.context)
	}
	if g.step != genNamespace {
		t.Fatalf("after context enter: step = %v, want genNamespace", g.step)
	}

	// Pick first namespace.
	g, _ = g.Update(pressKey("enter"))
	if g.namespace != "default" {
		t.Errorf("namespace = %q, want default", g.namespace)
	}
	if g.step != genOutDir {
		t.Fatalf("after namespace enter: step = %v, want genOutDir", g.step)
	}

	// Out-dir + confirm.
	g, _ = g.Update(pressKey("enter")) // → genConfirm
	g, _ = g.Update(pressKey("enter")) // dispatch
	if g.dispatchAction == nil {
		t.Fatalf("dispatchAction is nil after confirm; flash=%q", g.flash)
	}
	if !strings.Contains(g.dispatchCommand, "-k") {
		t.Errorf("cluster command should include -k, got %q", g.dispatchCommand)
	}
	if !strings.Contains(g.dispatchCommand, "--context staging") {
		t.Errorf("cluster command should include --context staging, got %q", g.dispatchCommand)
	}
	if !strings.Contains(g.dispatchCommand, "-n default") {
		t.Errorf("cluster command should include -n default, got %q", g.dispatchCommand)
	}
}

// TestGenerate_ClusterBranch_KubectlError covers the "no kubectl"
// path: the wizard surfaces the error inline instead of crashing,
// and Enter takes the user back to the source step so they can pick
// the file branch instead.
func TestGenerate_ClusterBranch_KubectlError(t *testing.T) {
	dir := t.TempDir()
	g := newGenerateModel(dir)
	g.kubectlRunner = &fakeKubectl{err: errors.New("kubectl: not found on PATH")}

	g, _ = g.Update(pressKey("down"))   // source = cluster
	g, _ = g.Update(pressKey("enter"))  // try to advance
	if g.step != genContext {
		t.Fatalf("step = %v, want genContext", g.step)
	}
	if g.contextErr == nil {
		t.Errorf("expected contextErr to be set")
	}
	// Enter on the error screen returns to the source step.
	g, _ = g.Update(pressKey("enter"))
	if g.step != genSource {
		t.Errorf("enter on error screen: step = %v, want genSource", g.step)
	}
}

// TestGenerate_OutDirDefault confirms the textinput is pre-filled
// with the model's outDir, so a user who entered the wizard to
// regenerate against the same dir doesn't have to retype it.
func TestGenerate_OutDirDefault(t *testing.T) {
	g := newGenerateModel("/tmp/preset-out")
	if v := g.outDir.Value(); v != "/tmp/preset-out" {
		t.Errorf("outDir default = %q, want /tmp/preset-out", v)
	}
}

// TestGenerate_BuildCommand_File pins the exact command shape the
// file branch produces, since changes to flag spelling would silently
// regress users.
func TestGenerate_BuildCommand_File(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "k8s"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := newGenerateModel("/tmp/out")
	g.source = genSourceFile
	g.filePath.SetValue(filepath.Join(dir, "k8s"))
	cmd, label, err := buildGenerateCommand(g)
	if err != nil {
		t.Fatalf("buildGenerateCommand: %v", err)
	}
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	if !strings.Contains(label, "generate") || !strings.Contains(label, "--out-dir /tmp/out") {
		t.Errorf("label missing pieces: %q", label)
	}
}

// TestGenerate_BuildCommand_Cluster pins the cluster-branch shape:
// must include -k -y so the subprocess doesn't prompt for the cluster
// confirmation (the TUI is the prompt).
func TestGenerate_BuildCommand_Cluster(t *testing.T) {
	g := newGenerateModel("/tmp/out")
	g.source = genSourceCluster
	g.context = "prod-eu1"
	g.namespace = "my-ns"
	_, label, err := buildGenerateCommand(g)
	if err != nil {
		t.Fatalf("buildGenerateCommand: %v", err)
	}
	for _, want := range []string{"-k", "-y", "--context prod-eu1", "-n my-ns", "--out-dir /tmp/out"} {
		if !strings.Contains(label, want) {
			t.Errorf("label missing %q: %s", want, label)
		}
	}
}
