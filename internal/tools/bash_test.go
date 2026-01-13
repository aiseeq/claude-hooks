package tools

import (
	"context"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func TestBashTool_BlocksDangerousCommands(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ToolConfig{
		Enabled:         true,
		BlockedPatterns: []string{"--headed", "rm -rf /", "rm -rf ~"},
	}

	tool, err := NewBashTool(config, logger)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	tests := []struct {
		name      string
		command   string
		wantBlock bool
	}{
		{
			name:      "blocks --headed flag",
			command:   "npx playwright test --headed",
			wantBlock: true,
		},
		{
			name:      "blocks rm -rf /",
			command:   "rm -rf /",
			wantBlock: true,
		},
		{
			name:      "blocks rm -rf ~",
			command:   "rm -rf ~",
			wantBlock: true,
		},
		{
			name:      "allows normal rm",
			command:   "rm -rf ./build",
			wantBlock: false,
		},
		{
			name:      "allows normal playwright",
			command:   "npx playwright test",
			wantBlock: false,
		},
		{
			name:      "allows git commands",
			command:   "git status",
			wantBlock: false,
		},
		{
			name:      "allows make commands",
			command:   "make build",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &core.ToolInput{
				ToolName: "Bash",
				Command:  tt.command,
			}

			result, err := tool.ValidateTool(context.Background(), input)
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}

			if tt.wantBlock && result.IsValid {
				t.Errorf("expected block but got pass for command: %s", tt.command)
			}
			if !tt.wantBlock && !result.IsValid {
				t.Errorf("expected pass but got block for command: %s", tt.command)
			}
		})
	}
}

func TestBashTool_Disabled(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ToolConfig{
		Enabled:         false,
		BlockedPatterns: []string{"--headed"},
	}

	tool, err := NewBashTool(config, logger)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	input := &core.ToolInput{
		ToolName: "Bash",
		Command:  "npx playwright test --headed",
	}

	result, err := tool.ValidateTool(context.Background(), input)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if !result.IsValid {
		t.Error("disabled tool should not block")
	}
}

func TestBashTool_IgnoresNonBashTools(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ToolConfig{
		Enabled:         true,
		BlockedPatterns: []string{"--headed"},
	}

	tool, err := NewBashTool(config, logger)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	input := &core.ToolInput{
		ToolName: "Write",
		Command:  "--headed", // Would be blocked if it was Bash
	}

	result, err := tool.ValidateTool(context.Background(), input)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if !result.IsValid {
		t.Error("should ignore non-Bash tools")
	}
}
