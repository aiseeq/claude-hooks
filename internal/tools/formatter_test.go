package tools

import (
	"context"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func TestFormatterTool_OnlyRunsInPostPhase(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ToolConfig{
		Enabled:  true,
		GoFormat: true,
	}

	tool, err := NewFormatterTool(config, logger)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	input := &core.ToolInput{
		ToolName: "Write",
		FilePath: "test.go",
	}

	// Test pre-tool-use phase - should skip
	preCtx := context.WithValue(context.Background(), "hook_phase", "pre")
	result, err := tool.ValidateTool(preCtx, input)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if !result.IsValid {
		t.Error("formatter should not block in pre phase")
	}
	if len(result.Violations) > 0 {
		t.Error("formatter should not run in pre phase")
	}
}

func TestFormatterTool_Disabled(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ToolConfig{
		Enabled: false,
	}

	tool, err := NewFormatterTool(config, logger)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	input := &core.ToolInput{
		ToolName: "Write",
		FilePath: "test.go",
	}

	postCtx := context.WithValue(context.Background(), "hook_phase", "post")
	result, err := tool.ValidateTool(postCtx, input)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if !result.IsValid {
		t.Error("disabled tool should not block")
	}
}

func TestFormatterTool_SkipsUnsupportedFiles(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ToolConfig{
		Enabled:  true,
		GoFormat: true,
		TSFormat: false, // TS formatting disabled
	}

	tool, err := NewFormatterTool(config, logger)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	// Test with unsupported file types
	tests := []struct {
		name     string
		filePath string
	}{
		{"markdown file", "README.md"},
		{"text file", "notes.txt"},
		{"yaml file", "config.yaml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &core.ToolInput{
				ToolName: "Write",
				FilePath: tt.filePath,
			}

			postCtx := context.WithValue(context.Background(), "hook_phase", "post")
			result, err := tool.ValidateTool(postCtx, input)
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}

			if !result.IsValid {
				t.Error("should not block unsupported files")
			}
		})
	}
}
