package visualize

import (
	"fmt"
	"io"
	"strings"
)

// EmitDOT writes a Graphviz DOT diagram source for g to w.
//
//revive:disable-next-line:cognitive-complexity EmitDOT is a linear sequence of subgraph emission then edge emission; splitting it would obscure the output structure.
func EmitDOT(g *Graph, w io.Writer) error {
	bw := &termWriter{w: w}

	bw.writeln("digraph pipeline {")
	bw.writeln(`  rankdir=TB;`)
	bw.writeln(`  compound=true;`)
	bw.writeln(`  node [shape=box fontname="monospace"];`)

	// Build sanitized job IDs.
	seen := make(map[string]int, len(g.Nodes))
	jobIDs := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		jobIDs[n.ID] = SanitizeID(n.ID, seen)
	}

	// Emit subgraph per job; collect ordered step node IDs for edge anchoring.
	stepIDs := emitDOTSubgraphs(bw, g.Nodes, jobIDs)

	for _, e := range g.Edges {
		emitDOTEdge(bw, e, jobIDs, stepIDs)
	}

	bw.writeln("}")
	if bw.err != nil {
		return fmt.Errorf("emitting DOT: %w", bw.err)
	}
	return nil
}

// emitDOTSubgraphs writes one cluster subgraph per node and returns
// job-raw-ID → ordered step DOT node IDs (pipeline declaration order).
func emitDOTSubgraphs(bw *termWriter, nodes []Node, jobIDs map[string]string) map[string][]string {
	stepIDs := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		jobID := jobIDs[n.ID]
		label := buildDOTJobLabel(n)
		bw.writeln(fmt.Sprintf("  subgraph cluster_%s {", jobID))
		bw.writeln(fmt.Sprintf("    label=%q;", label))
		sc := make(map[string]int, len(n.Steps))
		sids := make([]string, 0, len(n.Steps))
		for _, s := range n.Steps {
			sid := jobID + "__" + SanitizeID(s.Name, sc)
			sids = append(sids, sid)
			bw.writeln(fmt.Sprintf("    %s [label=%q];", sid, s.Name))
		}
		bw.writeln("  }")
		stepIDs[n.ID] = sids
	}
	return stepIDs
}

// emitDOTEdge writes a single edge in DOT syntax.
func emitDOTEdge(bw *termWriter, e Edge, jobIDs map[string]string, stepIDs map[string][]string) {
	fromJob := jobIDs[e.From]
	toJob := jobIDs[e.To]
	if fromJob == "" || toJob == "" {
		return
	}
	switch e.Kind {
	case DependsEdge:
		// Use real node endpoints with ltail/lhead so Graphviz clips the arrow
		// to the cluster boundary (requires compound=true).
		fromNode := dotFirstStep(stepIDs[e.From], "cluster_"+fromJob)
		toNode := dotFirstStep(stepIDs[e.To], "cluster_"+toJob)
		bw.writeln(fmt.Sprintf(`  %s -> %s [ltail="cluster_%s" lhead="cluster_%s"];`,
			fromNode, toNode, fromJob, toJob))
	case ArtifactEdge:
		fromNode := dotFirstStep(stepIDs[e.From], "cluster_"+fromJob)
		toNode := dotFirstStep(stepIDs[e.To], "cluster_"+toJob)
		bw.writeln(fmt.Sprintf("  %s -> %s [label=%q style=dashed];", fromNode, toNode, e.Label))
	default:
		// unknown edge kind; skip
	}
}

// dotFirstStep returns the first step DOT node ID in declaration order,
// or fallback when the job has no steps.
func dotFirstStep(steps []string, fallback string) string {
	if len(steps) > 0 {
		return steps[0]
	}
	return fallback
}

// buildDOTJobLabel constructs the label string for a subgraph.
func buildDOTJobLabel(n Node) string {
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
