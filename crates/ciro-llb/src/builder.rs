//! LLB builder implementation using lazy recursive construction.

use std::collections::{HashMap, HashSet};
use std::sync::Arc;

use buildkit_llb::prelude::{
    Command, Mount, MultiOwnedOutput, OperationBuilder, OutputIdx, SingleOwnedOutput, Source,
};
use buildkit_llb::utils::OperationOutput;
use ciro_core::{Pipeline, Step};

use crate::error::ConvertError;

/// Constant name for the local context source.
const CONTEXT_NAME: &str = "context";

/// Builder that converts a pipeline to LLB operations using lazy recursive construction.
///
/// Dependencies are built on-demand, which naturally provides:
/// - Correct ordering (deps built before dependents)
/// - Cycle detection (track "in progress" steps)
/// - Reference validation (lookup fails for missing deps)
pub struct LlbBuilder<'a> {
    /// Lookup table from step name to step reference.
    steps: HashMap<&'a str, &'a Step>,
    /// Memoized outputs for steps that have been built.
    built: HashMap<&'a str, OperationOutput<'static>>,
    /// Steps currently being built (for cycle detection).
    building: HashSet<&'a str>,
    /// Stack of step names for cycle path reconstruction.
    build_stack: Vec<&'a str>,
    /// Shared local context source for bind mounts.
    context: Arc<buildkit_llb::ops::source::LocalSource>,
}

impl<'a> LlbBuilder<'a> {
    /// Create a new builder from a pipeline.
    ///
    /// # Errors
    ///
    /// Returns `ConvertError::EmptyPipeline` if the pipeline has no steps.
    pub fn new(pipeline: &'a Pipeline) -> Result<Self, ConvertError> {
        if pipeline.steps.is_empty() {
            return Err(ConvertError::EmptyPipeline);
        }

        let steps = pipeline
            .steps
            .iter()
            .map(|step| (step.name.as_str(), step))
            .collect();

        Ok(Self {
            steps,
            built: HashMap::new(),
            building: HashSet::new(),
            build_stack: Vec::new(),
            context: Source::local(CONTEXT_NAME).ref_counted(),
        })
    }

    /// Build all steps in the pipeline and return the final outputs.
    ///
    /// # Errors
    ///
    /// Returns an error if any step fails to build due to:
    /// - Unknown dependencies
    /// - Dependency cycles
    /// - Empty steps (no commands)
    pub fn build_all(&mut self) -> Result<Vec<OperationOutput<'static>>, ConvertError> {
        let step_names: Vec<&str> = self.steps.keys().copied().collect();
        let mut outputs = Vec::with_capacity(step_names.len());

        for name in step_names {
            let output = self.build_step(name)?;
            outputs.push(output);
        }

        Ok(outputs)
    }

    /// Build a single step by name, recursively building dependencies first.
    ///
    /// # Errors
    ///
    /// Returns an error if:
    /// - The step does not exist (`UnknownDependency`)
    /// - A cycle is detected (`CycleDetected`)
    /// - The step has no commands (`EmptyStep`)
    fn build_step(&mut self, name: &'a str) -> Result<OperationOutput<'static>, ConvertError> {
        // Check memo cache first.
        if let Some(output) = self.built.get(name) {
            return Ok(output.clone());
        }

        // Look up the step.
        let step = *self.steps.get(name).ok_or_else(|| {
            // This path is hit when a dependency references a non-existent step.
            let parent = self.build_stack.last().copied().unwrap_or("unknown");
            ConvertError::UnknownDependency {
                step: parent.to_owned(),
                dependency: name.to_owned(),
            }
        })?;

        // Cycle detection.
        if self.building.contains(name) {
            // Find where the cycle starts in the stack.
            let cycle_start = self
                .build_stack
                .iter()
                .position(|&n| n == name)
                .unwrap_or(0);
            let mut path: Vec<String> = self.build_stack[cycle_start..]
                .iter()
                .map(|&s| s.to_owned())
                .collect();
            path.push(name.to_owned());
            return Err(ConvertError::CycleDetected { path });
        }

        // Validate step has commands.
        if step.run.is_empty() {
            return Err(ConvertError::EmptyStep {
                step: name.to_owned(),
            });
        }

        // Mark as building and add to stack.
        self.building.insert(name);
        self.build_stack.push(name);

        // Recursively build dependencies.
        for dep in &step.depends_on {
            self.build_step(dep.as_str())?;
        }

        // Build this step's LLB operations.
        let output = self.create_step_llb(step);

        // Remove from building state.
        self.building.remove(name);
        self.build_stack.pop();

        // Cache and return.
        self.built.insert(name, output.clone());
        Ok(output)
    }

    /// Create the LLB operations for a single step.
    fn create_step_llb(&self, step: &Step) -> OperationOutput<'static> {
        // Create the image source.
        let image = Source::image(&step.image).ref_counted();

        // Combine all run commands into a single shell invocation.
        let shell_cmd = step.run.join(" && ");

        // Build the command.
        let mut cmd = Command::run("/bin/sh")
            .args(["-c", &shell_cmd])
            .custom_name(&step.name);

        // Set working directory if specified.
        if let Some(ref workdir) = step.workdir {
            cmd = cmd.cwd(workdir);
        }

        // Add root filesystem mount (read-only from image).
        cmd = cmd.mount(Mount::ReadOnlyLayer(image.output(), "/"));

        // Add scratch mount for output.
        cmd = cmd.mount(Mount::Scratch(OutputIdx(0), "/"));

        // Add cache mounts.
        for cache in &step.caches {
            cmd = cmd.mount(Mount::SharedCache(&cache.target));
        }

        // Add bind mounts from local context.
        for mount in &step.mounts {
            cmd = cmd.mount(Mount::ReadOnlySelector(
                self.context.output(),
                &mount.target,
                &mount.source,
            ));
        }

        // Convert to Arc and return output.
        cmd.ref_counted().output(0)
    }
}
