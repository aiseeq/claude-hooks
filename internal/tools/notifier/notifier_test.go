package notifier

import (
	"context"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func TestNotifierTool_OnlyHandlesStop(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ToolConfig{
		Enabled: true,
	}

	tool, err := NewNotifierTool(config, logger)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	tests := []struct {
		name     string
		toolName string
	}{
		{"ignores Write", "Write"},
		{"ignores Edit", "Edit"},
		{"ignores Bash", "Bash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &core.ToolInput{
				ToolName: tt.toolName,
			}

			result, err := tool.ValidateTool(context.Background(), input)
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}

			if !result.IsValid {
				t.Errorf("notifier should not block non-Stop tools")
			}
			if len(result.Violations) > 0 {
				t.Errorf("notifier should not process non-Stop tools")
			}
		})
	}
}

func TestNotifierTool_Disabled(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ToolConfig{
		Enabled: false,
	}

	tool, err := NewNotifierTool(config, logger)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	input := &core.ToolInput{
		ToolName: "Stop",
	}

	result, err := tool.ValidateTool(context.Background(), input)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}

	if !result.IsValid {
		t.Error("disabled tool should not block")
	}
}

func TestNotifierTool_ExtractProjectName(t *testing.T) {
	logger := core.NewTestLogger()
	config := core.ToolConfig{
		Enabled: true,
		WorkDir: "/home/testuser/work",
	}

	tool, err := NewNotifierTool(config, logger)
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "extracts from direct path",
			path:     "/home/testuser/work/myproject/src/main.go",
			expected: "myproject",
		},
		{
			name:     "extracts project only",
			path:     "/home/testuser/work/saga",
			expected: "saga",
		},
		{
			name:     "returns unknown for unmatched path",
			path:     "/var/log/something",
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tool.extractProjectFromPath(tt.path)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestPathToEncoded(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/home/user/work", "home-user-work"},
		{"/var/log", "var-log"},
		{"relative/path", "relative-path"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := pathToEncoded(tt.path)
			if result != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, result)
			}
		})
	}
}
