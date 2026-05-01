package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/localk-dev/localk/internal/kubectl"
)

// genStep is the wizard's internal state machine. Steps progress
// linearly forward (Enter advances, validation gates each transition)
// and Esc walks back one step.
type genStep int

const (
	genSource    genStep = iota // [from file] / [from cluster]
	genFilePath                 // text input: input directory
	genContext                  // list of kubectl contexts
	genNamespace                // list of namespaces under chosen context
	genOutDir                   // text input: output directory
	genConfirm                  // summary; Enter dispatches the run
)

// genSourceChoice tracks which branch the user took at genSource.
// Affects what the wizard collects next and how the final command
// is built.
type genSourceChoice int

const (
	genSourceUnset genSourceChoice = iota
	genSourceFile
	genSourceCluster
)

// generateModel is the multi-step generate wizard. Owned by the
// parent Model; entered by selecting "Generate" from the top-level
// menu. The shared actionModel takes over for the actual subprocess
// run — we just collect the inputs.
type generateModel struct {
	step genStep

	// Source choice (file vs cluster).
	source     genSourceChoice
	sourceCur  int // cursor index for the 2-item source picker
	sourceList []string

	// File branch state.
	filePath textinput.Model

	// Cluster branch state.
	contexts    []string
	contextCur  int
	context     string
	contextErr  error
	namespaces  []string
	nsCur       int
	namespace   string
	namespaceErr error

	// Common.
	outDir textinput.Model
	flash  string // transient error / validation message

	// kubectl runner — abstracted so tests can inject a fake without
	// shelling out. Defaults to kubectl.Default() in newGenerateModel.
	kubectlRunner kubectl.Runner

	// requestExit signals the parent Model that the wizard is done
	// (Esc from genSource, or after dispatching to actionModel). The
	// parent reads it in its Update wrapper.
	requestExit bool
	// dispatchAction, when non-nil, holds an exec.Cmd for the parent
	// to hand off to the actionModel + tea.ExecProcess. Set when the
	// user confirms; cleared by reset() before the next entry.
	dispatchAction *exec.Cmd
	// dispatchTitle / dispatchCommand decorate the post-run screen.
	dispatchTitle   string
	dispatchCommand string
}

func newGenerateModel(outDir string) generateModel {
	fileInput := textinput.New()
	fileInput.Placeholder = "path to k8s manifests (e.g. ./k8s)"
	fileInput.CharLimit = 256
	fileInput.Width = 50

	outInput := textinput.New()
	outInput.Placeholder = "output directory"
	outInput.CharLimit = 256
	outInput.Width = 50
	outInput.SetValue(outDir)

	return generateModel{
		step:          genSource,
		sourceList:    []string{"From local files", "From kubectl cluster"},
		filePath:      fileInput,
		outDir:        outInput,
		kubectlRunner: kubectl.Default(),
	}
}

// reset clears one-shot signals before re-entering the wizard from
// the menu. Preserves the user's previously-entered paths so they
// don't have to retype on a second run.
func (g *generateModel) reset() {
	g.requestExit = false
	g.dispatchAction = nil
	g.dispatchTitle = ""
	g.dispatchCommand = ""
	g.flash = ""
	g.step = genSource
}

func (g generateModel) Update(msg tea.KeyMsg) (generateModel, tea.Cmd) {
	g.flash = ""

	// Esc backs up one step (or exits the wizard from step 0).
	if msg.String() == "esc" {
		switch g.step {
		case genSource:
			g.requestExit = true
		case genFilePath:
			g.step = genSource
		case genContext:
			g.step = genSource
		case genNamespace:
			g.step = genContext
		case genOutDir:
			if g.source == genSourceFile {
				g.step = genFilePath
			} else {
				g.step = genNamespace
			}
		case genConfirm:
			g.step = genOutDir
		}
		return g, nil
	}

	switch g.step {
	case genSource:
		return g.updateSource(msg)
	case genFilePath:
		return g.updateFilePath(msg)
	case genContext:
		return g.updateContext(msg)
	case genNamespace:
		return g.updateNamespace(msg)
	case genOutDir:
		return g.updateOutDir(msg)
	case genConfirm:
		return g.updateConfirm(msg)
	}
	return g, nil
}

func (g generateModel) updateSource(msg tea.KeyMsg) (generateModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if g.sourceCur > 0 {
			g.sourceCur--
		}
	case "down", "j":
		if g.sourceCur < len(g.sourceList)-1 {
			g.sourceCur++
		}
	case "enter":
		if g.sourceCur == 0 {
			g.source = genSourceFile
			g.step = genFilePath
			g.filePath.Focus()
			return g, textinput.Blink
		}
		g.source = genSourceCluster
		g.step = genContext
		// Lazy-load contexts when the user picks the cluster path.
		// Returns an error inline rather than crashing if kubectl is
		// missing or the kubeconfig has no contexts.
		if err := kubectl.Available(g.kubectlRunner); err != nil {
			g.contextErr = err
			g.contexts = nil
			return g, nil
		}
		ctxs, err := kubectl.ListContexts(g.kubectlRunner)
		if err != nil {
			g.contextErr = err
			g.contexts = nil
			return g, nil
		}
		g.contextErr = nil
		g.contexts = ctxs
		g.contextCur = 0
	}
	return g, nil
}

func (g generateModel) updateFilePath(msg tea.KeyMsg) (generateModel, tea.Cmd) {
	if msg.String() == "enter" {
		path := strings.TrimSpace(g.filePath.Value())
		if path == "" {
			g.flash = "enter a path"
			return g, nil
		}
		info, err := os.Stat(path)
		if err != nil {
			g.flash = fmt.Sprintf("path not found: %v", err)
			return g, nil
		}
		if !info.IsDir() {
			g.flash = "path must be a directory containing manifest YAML files"
			return g, nil
		}
		g.filePath.Blur()
		g.outDir.Focus()
		g.step = genOutDir
		return g, textinput.Blink
	}
	var cmd tea.Cmd
	g.filePath, cmd = g.filePath.Update(msg)
	return g, cmd
}

func (g generateModel) updateContext(msg tea.KeyMsg) (generateModel, tea.Cmd) {
	if g.contextErr != nil || len(g.contexts) == 0 {
		// No contexts available — Enter goes back to source.
		if msg.String() == "enter" {
			g.step = genSource
		}
		return g, nil
	}
	switch msg.String() {
	case "up", "k":
		if g.contextCur > 0 {
			g.contextCur--
		}
	case "down", "j":
		if g.contextCur < len(g.contexts)-1 {
			g.contextCur++
		}
	case "enter":
		g.context = g.contexts[g.contextCur]
		g.step = genNamespace
		nss, err := kubectl.ListNamespaces(g.kubectlRunner, g.context)
		if err != nil {
			g.namespaceErr = err
			g.namespaces = nil
			return g, nil
		}
		g.namespaceErr = nil
		g.namespaces = nss
		g.nsCur = 0
	}
	return g, nil
}

func (g generateModel) updateNamespace(msg tea.KeyMsg) (generateModel, tea.Cmd) {
	if g.namespaceErr != nil || len(g.namespaces) == 0 {
		if msg.String() == "enter" {
			g.step = genContext
		}
		return g, nil
	}
	switch msg.String() {
	case "up", "k":
		if g.nsCur > 0 {
			g.nsCur--
		}
	case "down", "j":
		if g.nsCur < len(g.namespaces)-1 {
			g.nsCur++
		}
	case "enter":
		g.namespace = g.namespaces[g.nsCur]
		g.outDir.Focus()
		g.step = genOutDir
		return g, textinput.Blink
	}
	return g, nil
}

func (g generateModel) updateOutDir(msg tea.KeyMsg) (generateModel, tea.Cmd) {
	if msg.String() == "enter" {
		v := strings.TrimSpace(g.outDir.Value())
		if v == "" {
			g.flash = "enter an output directory"
			return g, nil
		}
		g.outDir.Blur()
		g.step = genConfirm
		return g, nil
	}
	var cmd tea.Cmd
	g.outDir, cmd = g.outDir.Update(msg)
	return g, cmd
}

func (g generateModel) updateConfirm(msg tea.KeyMsg) (generateModel, tea.Cmd) {
	if msg.String() != "enter" {
		return g, nil
	}
	cmd, label, err := buildGenerateCommand(g)
	if err != nil {
		g.flash = err.Error()
		return g, nil
	}
	g.dispatchAction = cmd
	g.dispatchTitle = "Generate"
	g.dispatchCommand = label
	return g, nil
}

// buildGenerateCommand assembles the actual `localk generate ...`
// invocation the user just configured. Returns the exec.Cmd plus a
// human-readable label for the post-run screen. Returns an error
// when localk's own path can't be resolved (extremely unlikely under
// normal use).
func buildGenerateCommand(g generateModel) (*exec.Cmd, string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, "", fmt.Errorf("locate localk binary: %w", err)
	}
	args := []string{"generate"}
	if g.source == genSourceCluster {
		args = append(args, "-k", "-y") // -y skips the interactive prompt
		if g.context != "" {
			args = append(args, "--context", g.context)
		}
		if g.namespace != "" {
			args = append(args, "-n", g.namespace)
		}
	} else {
		args = append(args, strings.TrimSpace(g.filePath.Value()))
	}
	args = append(args, "--out-dir", strings.TrimSpace(g.outDir.Value()))
	cmd := exec.Command(self, args...)
	label := self + " " + strings.Join(args, " ")
	return cmd, label, nil
}

// View renders the wizard's current step.
func (g generateModel) View(width int) string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("localk") + subHeaderStyle.Render(" — generate"))
	b.WriteString("\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", maxWidth(width, 80))))
	b.WriteString("\n\n")

	switch g.step {
	case genSource:
		b.WriteString(subHeaderStyle.Render("Where are the k8s manifests?"))
		b.WriteString("\n\n")
		for i, label := range g.sourceList {
			cursor := "  "
			styled := label
			if i == g.sourceCur {
				cursor = cursorStyle.Render("▶ ")
				styled = cursorStyle.Render(label)
			}
			b.WriteString(cursor + styled + "\n")
		}
	case genFilePath:
		b.WriteString(subHeaderStyle.Render("Path to manifest directory:"))
		b.WriteString("\n\n  ")
		b.WriteString(g.filePath.View())
	case genContext:
		b.WriteString(subHeaderStyle.Render("Choose kubectl context:"))
		b.WriteString("\n\n")
		if g.contextErr != nil {
			b.WriteString(errorStyle.Render("✗ " + g.contextErr.Error()))
			b.WriteString("\n  press enter to go back")
		} else if len(g.contexts) == 0 {
			b.WriteString(errorStyle.Render("✗ no kubectl contexts found"))
			b.WriteString("\n  press enter to go back")
		} else {
			for i, name := range g.contexts {
				cursor := "  "
				styled := name
				if i == g.contextCur {
					cursor = cursorStyle.Render("▶ ")
					styled = cursorStyle.Render(name)
				}
				b.WriteString(cursor + styled + "\n")
			}
		}
	case genNamespace:
		b.WriteString(subHeaderStyle.Render(fmt.Sprintf("Choose namespace in %q:", g.context)))
		b.WriteString("\n\n")
		if g.namespaceErr != nil {
			b.WriteString(errorStyle.Render("✗ " + g.namespaceErr.Error()))
			b.WriteString("\n  press enter to go back")
		} else if len(g.namespaces) == 0 {
			b.WriteString(errorStyle.Render("✗ no namespaces found"))
			b.WriteString("\n  press enter to go back")
		} else {
			for i, name := range g.namespaces {
				cursor := "  "
				styled := name
				if i == g.nsCur {
					cursor = cursorStyle.Render("▶ ")
					styled = cursorStyle.Render(name)
				}
				b.WriteString(cursor + styled + "\n")
			}
		}
	case genOutDir:
		b.WriteString(subHeaderStyle.Render("Output directory:"))
		b.WriteString("\n\n  ")
		b.WriteString(g.outDir.View())
	case genConfirm:
		b.WriteString(subHeaderStyle.Render("Ready to generate. Review and confirm:"))
		b.WriteString("\n\n")
		if g.source == genSourceCluster {
			b.WriteString(fmt.Sprintf("  source       cluster (context %q, namespace %q)\n", g.context, g.namespace))
		} else {
			b.WriteString(fmt.Sprintf("  source       %s\n", g.filePath.Value()))
		}
		b.WriteString(fmt.Sprintf("  out-dir      %s\n", g.outDir.Value()))
	}

	if g.flash != "" {
		b.WriteString("\n\n")
		b.WriteString(flashStyle.Render(g.flash))
	}

	b.WriteString("\n\n")
	b.WriteString(dividerStyle.Render(strings.Repeat("─", maxWidth(width, 80))))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render(generateFooter(g.step)))
	return b.String()
}

func generateFooter(step genStep) string {
	switch step {
	case genSource, genContext, genNamespace:
		return "↑/↓ navigate  ·  enter select  ·  esc back  ·  ctrl+c quit"
	case genFilePath, genOutDir:
		return "type a path  ·  enter confirm  ·  esc back  ·  ctrl+c quit"
	case genConfirm:
		return "enter run  ·  esc back  ·  ctrl+c quit"
	}
	return "esc back  ·  ctrl+c quit"
}
