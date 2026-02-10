package parser

// NodeType represents the type of a KDL node.
type NodeType string

// Node types.
const (
	NodeTypePipeline  NodeType = "pipeline"
	NodeTypeStep      NodeType = "step"
	NodeTypeMatrix    NodeType = "matrix"
	NodeTypeMount     NodeType = "mount"
	NodeTypeCache     NodeType = "cache"
	NodeTypeImage     NodeType = "image"
	NodeTypeRun       NodeType = "run"
	NodeTypeWorkdir   NodeType = "workdir"
	NodeTypeDependsOn NodeType = "depends-on"
)

// PropReadonly is the property key for mount read-only mode.
const PropReadonly = "readonly"
