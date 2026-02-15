package parser

// NodeType represents the type of a KDL node.
type NodeType string

// Node types.
const (
	NodeTypeJob       NodeType = "job"
	NodeTypeStep      NodeType = "step"
	NodeTypeDefaults  NodeType = "defaults"
	NodeTypeMatrix    NodeType = "matrix"
	NodeTypeMount     NodeType = "mount"
	NodeTypeCache     NodeType = "cache"
	NodeTypeImage     NodeType = "image"
	NodeTypeRun       NodeType = "run"
	NodeTypeWorkdir   NodeType = "workdir"
	NodeTypeDependsOn NodeType = "depends-on"
	NodeTypePlatform  NodeType = "platform"
	NodeTypeInclude   NodeType = "include"
	NodeTypeFragment  NodeType = "fragment"
	NodeTypeParam     NodeType = "param"
	NodeTypeEnv       NodeType = "env"
	NodeTypeExport    NodeType = "export"
	NodeTypeArtifact  NodeType = "artifact"
	NodeTypeNoCache   NodeType = "no-cache"
	NodeTypePublish   NodeType = "publish"
)

// Property keys.
const (
	PropReadonly   = "readonly"
	PropOnConflict = "on-conflict"
	PropDefault    = "default"
	PropAs         = "as"
	PropLocal      = "local"
	PropPush       = "push"
	PropInsecure   = "insecure"
)
