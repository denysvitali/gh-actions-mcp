package mcp

// Tool declarations for the artifact family. get_artifact streams contents into
// the response; download_artifact is the only tool that writes to local disk and
// is therefore the only artifact tool marked destructive.

// registerArtifactReadTools declares get_artifact.
func (s *MCPServer) registerArtifactReadTools(b toolBuilder) {
	// Tool: get_artifact
	addTool(s, b.NewTool("get_artifact",
		b.WithDescription("Get the contents of a workflow run artifact (stream without downloading to disk)"),
		b.ReadOnly(),
		b.repoOverrides(),
		b.WithNumber("artifact_id",
			b.Description("The artifact ID"),
			b.Required(),
		),
		b.WithString("file_pattern",
			b.Description("Optional: glob pattern to filter files within the artifact (e.g., '*.txt', 'logs/*.log')"),
		),
		b.WithNumber("max_file_size",
			b.Description("Optional: maximum size of individual files to read in bytes (default: 1MB). Files larger than this will show size info only."),
			b.DefaultNumber(defaultMaxArtifactFileSize),
		),
	), s.getArtifactTyped)
}

// registerArtifactDownloadTools declares download_artifact.
func (s *MCPServer) registerArtifactDownloadTools(b toolBuilder) {
	// Tool: download_artifact
	addTool(s, b.NewTool("download_artifact",
		b.WithDescription("Download a workflow run artifact to disk"),
		b.Destructive(),
		b.repoOverrides(),
		b.WithNumber("artifact_id",
			b.Description("The artifact ID"),
			b.Required(),
		),
		b.WithString("output_path",
			b.Description("Optional path relative to artifact_root (default: {artifact-name}.zip)"),
		),
		b.WithBoolean("overwrite",
			b.Description("Replace an existing destination atomically (default: false)"),
		),
	), s.downloadArtifactTyped)
}
