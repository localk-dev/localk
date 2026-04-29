package kube

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// ParseDir walks a directory recursively and parses every .yaml/.yml file
// it finds, returning a Manifests bundle of recognized resources.
func ParseDir(root string) (*Manifests, error) {
	m := &Manifests{}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		return parseFile(path, m)
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", root, err)
	}
	return m, nil
}

// parseFile parses a single YAML file (which may contain multiple documents
// separated by `---`) and appends recognized resources to m.
//
// Implementation note: we split the file on document separators ourselves so
// that each document's bytes can be decoded twice — once into a generic
// envelope to learn the Kind, then once more into the specific typed resource.
// This is simpler than navigating the goccy AST.
func parseFile(path string, m *Manifests) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	docs, err := splitDocuments(data)
	if err != nil {
		return fmt.Errorf("splitting %s into documents: %w", path, err)
	}

	for _, doc := range docs {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		var envelope Resource
		if err := yaml.Unmarshal(doc, &envelope); err != nil {
			return fmt.Errorf("decoding envelope in %s: %w", path, err)
		}
		if envelope.Kind == "" {
			continue
		}

		switch envelope.Kind {
		case "Deployment":
			var d Deployment
			if err := yaml.Unmarshal(doc, &d); err != nil {
				return fmt.Errorf("decoding Deployment in %s: %w", path, err)
			}
			m.Deployments = append(m.Deployments, d)
		case "Service":
			var s Service
			if err := yaml.Unmarshal(doc, &s); err != nil {
				return fmt.Errorf("decoding Service in %s: %w", path, err)
			}
			m.Services = append(m.Services, s)
		case "ConfigMap":
			var c ConfigMap
			if err := yaml.Unmarshal(doc, &c); err != nil {
				return fmt.Errorf("decoding ConfigMap in %s: %w", path, err)
			}
			m.ConfigMaps = append(m.ConfigMaps, c)
		case "Secret":
			var s Secret
			if err := yaml.Unmarshal(doc, &s); err != nil {
				return fmt.Errorf("decoding Secret in %s: %w", path, err)
			}
			m.Secrets = append(m.Secrets, s)
		case "PersistentVolumeClaim":
			var p PersistentVolumeClaim
			if err := yaml.Unmarshal(doc, &p); err != nil {
				return fmt.Errorf("decoding PVC in %s: %w", path, err)
			}
			m.PVCs = append(m.PVCs, p)
		default:
			// Silently ignore unsupported kinds for now.
		}
	}
	return nil
}

// splitDocuments splits a YAML byte stream on document separators ("---" on
// its own line).
func splitDocuments(data []byte) ([][]byte, error) {
	var docs [][]byte
	var current bytes.Buffer

	r := bytes.NewReader(data)
	br := newLineReader(r)
	for {
		line, err := br.ReadLine()
		if err != nil && err != io.EOF {
			return nil, err
		}
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "---" {
			docs = append(docs, append([]byte(nil), current.Bytes()...))
			current.Reset()
			if err == io.EOF {
				break
			}
			continue
		}
		current.Write(line)
		if err == io.EOF {
			break
		}
	}
	docs = append(docs, append([]byte(nil), current.Bytes()...))
	return docs, nil
}

// lineReader reads one line at a time including its terminator so that
// rejoining the bytes preserves the original layout.
type lineReader struct {
	r io.Reader
}

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: r} }

func (lr *lineReader) ReadLine() ([]byte, error) {
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := lr.r.Read(one)
		if n > 0 {
			line = append(line, one[0])
			if one[0] == '\n' {
				return line, nil
			}
		}
		if err != nil {
			return line, err
		}
	}
}

// FindConfigMap returns the ConfigMap with the given name, or nil if not found.
func (m *Manifests) FindConfigMap(name string) *ConfigMap {
	for i := range m.ConfigMaps {
		if m.ConfigMaps[i].Metadata.Name == name {
			return &m.ConfigMaps[i]
		}
	}
	return nil
}

// FindSecret returns the Secret with the given name, or nil if not found.
func (m *Manifests) FindSecret(name string) *Secret {
	for i := range m.Secrets {
		if m.Secrets[i].Metadata.Name == name {
			return &m.Secrets[i]
		}
	}
	return nil
}

// FindPVC returns the PVC with the given name, or nil if not found.
func (m *Manifests) FindPVC(name string) *PersistentVolumeClaim {
	for i := range m.PVCs {
		if m.PVCs[i].Metadata.Name == name {
			return &m.PVCs[i]
		}
	}
	return nil
}

// FindServiceForSelector returns the first Service whose selector matches the
// given pod template labels, or nil if none does.
func (m *Manifests) FindServiceForSelector(podLabels map[string]string) *Service {
	for i := range m.Services {
		sel := m.Services[i].Spec.Selector
		if len(sel) == 0 {
			continue
		}
		if labelsMatch(sel, podLabels) {
			return &m.Services[i]
		}
	}
	return nil
}

func labelsMatch(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
