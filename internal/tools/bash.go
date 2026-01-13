package tools

import (
	"context"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// BashTool validator for bash commands
type BashTool struct {
	*BaseTool
	blockedPatterns []string
}

// NewBashTool creates new bash tool validator
func NewBashTool(config core.ToolConfig, logger core.Logger) (*BashTool, error) {
	base := NewBaseTool("bash", config.Enabled, []string{"Bash"}, logger)

	// Use BlockedPatterns from config, fallback to DangerousCommands for backwards compatibility
	blockedPatterns := config.BlockedPatterns
	if len(blockedPatterns) == 0 {
		blockedPatterns = config.DangerousCommands
	}

	tool := &BashTool{
		BaseTool:        base,
		blockedPatterns: blockedPatterns,
	}

	return tool, nil
}

// ValidateTool checks bash commands for dangerous patterns
func (t *BashTool) ValidateTool(ctx context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	if !t.IsEnabled() {
		return &core.ValidationResult{IsValid: true}, nil
	}

	if input.ToolName != "Bash" {
		return &core.ValidationResult{IsValid: true}, nil
	}

	command := extractCommand(input)
	if command == "" {
		return &core.ValidationResult{IsValid: true}, nil
	}

	t.logger.Debug("validating bash command", "command", command)

	var violations []core.Violation

	// Check for blocked patterns
	for _, pattern := range t.blockedPatterns {
		if strings.Contains(command, pattern) {
			violation := core.Violation{
				Type:       "dangerous_bash_command",
				Message:    "Dangerous bash command detected: " + pattern,
				Suggestion: "Avoid potentially destructive commands",
				Severity:   core.LevelCritical,
				Line:       1,
				Column:     strings.Index(command, pattern) + 1,
			}
			violations = append(violations, violation)
		}
	}

	isValid := len(violations) == 0

	return &core.ValidationResult{
		IsValid:    isValid,
		Violations: violations,
	}, nil
}
