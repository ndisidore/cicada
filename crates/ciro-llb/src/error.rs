//! Error types for LLB conversion.

use thiserror::Error;

/// Errors that can occur during pipeline to LLB conversion.
#[derive(Debug, Error)]
pub enum ConvertError {
    /// A step references a dependency that does not exist.
    #[error("step `{step}` depends on unknown step `{dependency}`")]
    UnknownDependency {
        /// The step containing the invalid reference.
        step: String,
        /// The missing dependency name.
        dependency: String,
    },

    /// A cycle was detected in the dependency graph.
    #[error("dependency cycle detected: {}", .path.join(" -> "))]
    CycleDetected {
        /// The path of steps forming the cycle.
        path: Vec<String>,
    },

    /// A step has no commands to execute.
    #[error("step `{step}` has no commands")]
    EmptyStep {
        /// The name of the empty step.
        step: String,
    },

    /// The pipeline contains no steps.
    #[error("pipeline has no steps")]
    EmptyPipeline,
}
