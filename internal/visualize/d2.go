package visualize

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// IsSupportedOutputExt reports whether ext (lowercased, with leading dot) is a
// file extension accepted by EmitToFile.
func IsSupportedOutputExt(ext string) bool {
	return ext == ".d2" || ext == ".dot"
}

// EmitToFile dispatches to the correct renderer based on outPath's extension.
// Supported extensions: .d2, .dot.
func EmitToFile(g *Graph, outPath string, w io.Writer) error {
	ext := strings.ToLower(filepath.Ext(outPath))
	switch ext {
	case ".d2":
		return EmitD2(g, w)
	case ".dot":
		return EmitDOT(g, w)
	default:
		return fmt.Errorf("unsupported output format %q (use .d2 or .dot)", filepath.Ext(outPath))
	}
}

// EmitD2 writes a D2 diagram source for g to w.
func EmitD2(g *Graph, w io.Writer) error {
	bw := &termWriter{w: w}

	if g.Title != "" {
		bw.writeln("# Pipeline: " + g.Title)
	}

	// Job IDs share a single seen map so duplicates across jobs are resolved.
	seen := make(map[string]int, len(g.Nodes))
	jobIDs := make(map[string]string, len(g.Nodes))

	for _, n := range g.Nodes {
		jobID := SanitizeID(n.ID, seen)
		jobIDs[n.ID] = jobID
		label := buildD2Label(n)
		bw.writeln(jobID + ": {")
		bw.writeln(fmt.Sprintf("  label: %q", label))
		// Step IDs are scoped inside their container; use a fresh map per job.
		stepSeen := make(map[string]int, len(n.Steps))
		for _, s := range n.Steps {
			stepID := SanitizeID(s.Name, stepSeen)
			stepLabel := s.Name
			if s.AllowFailure {
				stepLabel += " [!]"
			}
			bw.writeln(fmt.Sprintf("  %s: {label: %q}", stepID, stepLabel))
		}
		bw.writeln("}")
	}

	for _, e := range g.Edges {
		fromID := jobIDs[e.From]
		toID := jobIDs[e.To]
		if fromID == "" || toID == "" {
			continue
		}
		switch e.Kind {
		case DependsEdge:
			bw.writeln(fromID + " -> " + toID)
		case ArtifactEdge:
			bw.writeln(fmt.Sprintf("%s -> %s: %q {", fromID, toID, e.Label))
			bw.writeln("  style.stroke-dash: 5")
			bw.writeln("}")
		default:
			// unknown edge kind; skip
		}
	}

	if bw.err != nil {
		return fmt.Errorf("d2 write: %w", bw.err)
	}
	return nil
}

// buildD2Label constructs the multi-line label string for a job container.
func buildD2Label(n Node) string {
	var parts []string
	parts = append(parts, n.ID)
	if n.When != "" {
		parts = append(parts, "[when: "+n.When+"]")
	}
	for _, m := range n.Matrix {
		parts = append(parts, "[matrix: "+m+"]")
	}
	if n.HasSecrets {
		parts = append(parts, "[secrets]")
	}
	if n.Publish != "" {
		parts = append(parts, "[publish: "+n.Publish+"]")
	}
	return strings.Join(parts, "\n")
}
