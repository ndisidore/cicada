//! LLB generation for `BuildKit`.
//!
//! This crate converts `ciro_core::Pipeline` definitions into `BuildKit` LLB
//! (`pb::Definition`) that can be sent to a `BuildKit` daemon for execution.
//!
//! # Example
//!
//! ```ignore
//! use ciro_core::Pipeline;
//! use ciro_llb::to_definition;
//!
//! let pipeline = Pipeline { /* ... */ };
//! let definition = to_definition(&pipeline)?;
//! // Send definition to BuildKit daemon
//! ```

mod builder;
mod error;

pub use error::ConvertError;

use buildkit_llb::prelude::Terminal;
use buildkit_proto::pb;
use ciro_core::Pipeline;

/// Convert a pipeline to a `BuildKit` LLB definition.
///
/// This function traverses the pipeline's step dependency graph, building
/// LLB operations for each step. Dependencies are resolved lazily, ensuring
/// correct ordering and detecting cycles.
///
/// # Arguments
///
/// * `pipeline` - The pipeline to convert.
///
/// # Returns
///
/// A `pb::Definition` containing the serialized LLB operations and metadata.
///
/// # Errors
///
/// Returns `ConvertError` if:
/// - The pipeline is empty (`EmptyPipeline`)
/// - A step references a non-existent dependency (`UnknownDependency`)
/// - A dependency cycle is detected (`CycleDetected`)
/// - A step has no commands (`EmptyStep`)
///
/// # Example
///
/// ```
/// use ciro_core::{Pipeline, Step};
/// use ciro_llb::to_definition;
///
/// let mut step = Step::new("build", "rust:1.75");
/// step.run.push("cargo build --release".to_string());
///
/// let pipeline = Pipeline {
///     name: "example".to_string(),
///     steps: vec![step],
/// };
///
/// let definition = to_definition(&pipeline).unwrap();
/// assert!(!definition.def.is_empty());
/// ```
pub fn to_definition(pipeline: &Pipeline) -> Result<pb::Definition, ConvertError> {
    let mut builder = builder::LlbBuilder::new(pipeline)?;
    let outputs = builder.build_all()?;

    // Use the last output as the terminal operation.
    // All steps are built, so we can pick any leaf. Using the last maintains
    // a predictable order.
    let last_output = outputs
        .into_iter()
        .last()
        .ok_or(ConvertError::EmptyPipeline)?;

    Ok(Terminal::with(last_output).into_definition())
}

#[cfg(test)]
mod tests {
    use ciro_core::{Cache, Mount, Pipeline, Step};

    use super::*;

    fn simple_step(name: &str, image: &str, cmd: &str) -> Step {
        let mut step = Step::new(name, image);
        step.run.push(cmd.to_string());
        step
    }

    #[test]
    fn single_step_pipeline() {
        let pipeline = Pipeline {
            name: "test".to_string(),
            steps: vec![simple_step("build", "alpine:latest", "echo hello")],
        };

        let result = to_definition(&pipeline);
        assert!(result.is_ok());
        let def = result.unwrap();
        assert!(!def.def.is_empty());
    }

    #[test]
    fn multi_step_with_dependencies() {
        let step1 = simple_step("setup", "alpine:latest", "echo setup");
        let mut step2 = simple_step("build", "alpine:latest", "echo build");
        step2.depends_on.push("setup".to_string());

        let pipeline = Pipeline {
            name: "test".to_string(),
            steps: vec![step1, step2],
        };

        let result = to_definition(&pipeline);
        assert!(result.is_ok());
    }

    #[test]
    fn cycle_detection() {
        let mut step_a = simple_step("a", "alpine:latest", "echo a");
        let mut step_b = simple_step("b", "alpine:latest", "echo b");
        step_a.depends_on.push("b".to_string());
        step_b.depends_on.push("a".to_string());

        let pipeline = Pipeline {
            name: "test".to_string(),
            steps: vec![step_a, step_b],
        };

        let result = to_definition(&pipeline);
        assert!(matches!(result, Err(ConvertError::CycleDetected { .. })));

        if let Err(ConvertError::CycleDetected { path }) = result {
            // Path should show the cycle: a -> b -> a or b -> a -> b
            assert!(path.len() >= 2);
            assert_eq!(path.first(), path.last());
        }
    }

    #[test]
    fn unknown_dependency() {
        let mut step = simple_step("build", "alpine:latest", "echo build");
        step.depends_on.push("nonexistent".to_string());

        let pipeline = Pipeline {
            name: "test".to_string(),
            steps: vec![step],
        };

        let result = to_definition(&pipeline);
        assert!(matches!(
            result,
            Err(ConvertError::UnknownDependency { .. })
        ));

        if let Err(ConvertError::UnknownDependency { step, dependency }) = result {
            assert_eq!(step, "build");
            assert_eq!(dependency, "nonexistent");
        }
    }

    #[test]
    fn empty_step_error() {
        let step = Step::new("empty", "alpine:latest");

        let pipeline = Pipeline {
            name: "test".to_string(),
            steps: vec![step],
        };

        let result = to_definition(&pipeline);
        assert!(matches!(result, Err(ConvertError::EmptyStep { .. })));

        if let Err(ConvertError::EmptyStep { step }) = result {
            assert_eq!(step, "empty");
        }
    }

    #[test]
    fn empty_pipeline_error() {
        let pipeline = Pipeline {
            name: "test".to_string(),
            steps: vec![],
        };

        let result = to_definition(&pipeline);
        assert!(matches!(result, Err(ConvertError::EmptyPipeline)));
    }

    #[test]
    fn step_with_cache_mounts() {
        let mut step = simple_step("build", "rust:1.75", "cargo build");
        step.caches.push(Cache {
            id: "cargo".to_string(),
            target: "/root/.cargo".to_string(),
        });

        let pipeline = Pipeline {
            name: "test".to_string(),
            steps: vec![step],
        };

        let result = to_definition(&pipeline);
        assert!(result.is_ok());
    }

    #[test]
    fn step_with_bind_mounts() {
        let mut step = simple_step("build", "alpine:latest", "cat /src/file.txt");
        step.mounts.push(Mount {
            source: "src".to_string(),
            target: "/src".to_string(),
        });

        let pipeline = Pipeline {
            name: "test".to_string(),
            steps: vec![step],
        };

        let result = to_definition(&pipeline);
        assert!(result.is_ok());
    }

    #[test]
    fn step_with_workdir() {
        let mut step = simple_step("build", "alpine:latest", "pwd");
        step.workdir = Some("/app".to_string());

        let pipeline = Pipeline {
            name: "test".to_string(),
            steps: vec![step],
        };

        let result = to_definition(&pipeline);
        assert!(result.is_ok());
    }
}
