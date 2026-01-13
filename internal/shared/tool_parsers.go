package shared

import (
	"encoding/json"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// ToolInputParams typed structure для замены interface{} usage
// Следует принципам CLAUDE.md TYPE SAFETY FIRST
type ToolInputParams struct {
	FilePath  string      `json:"filePath"`
	Content   string      `json:"content"`
	NewString string      `json:"newString"`
	Edits     []EditParam `json:"edits"`
}

// EditParam параметр для MultiEdit операций
type EditParam struct {
	NewString string `json:"newString"`
	OldString string `json:"oldString"`
}

// ParseToolInputContent извлекает содержимое и путь файла из tool input
// CANONICAL VERSION - заменяет getContentFromTool дубликаты в config.go и naming.go
// Использует type-safe structs вместо interface{} согласно CLAUDE.md
func ParseToolInputContent(toolInput *core.ToolInput) (string, string, error) {
	// Parse tool input JSON to extract parameters with type safety
	var toolParams ToolInputParams
	if err := json.Unmarshal(toolInput.ToolInput, &toolParams); err != nil {
		return "", "", err
	}

	switch toolInput.ToolName {
	case "Write", "Edit", "MultiEdit":
		// Прямое содержимое из content поля
		if toolParams.Content != "" {
			return toolParams.Content, toolParams.FilePath, nil
		}

		// Для Edit и MultiEdit может быть в new_string
		if toolParams.NewString != "" {
			return toolParams.NewString, toolParams.FilePath, nil
		}

		// Для MultiEdit может быть массив edits
		if len(toolParams.Edits) > 0 {
			var allContent strings.Builder
			for _, edit := range toolParams.Edits {
				if edit.NewString != "" {
					allContent.WriteString(edit.NewString)
					allContent.WriteString("\n")
				}
			}
			return allContent.String(), toolParams.FilePath, nil
		}
	}

	return "", "", core.ErrUnsupportedTool
}

// CreateFakeToolInput создает fake ToolInput для анализа content
// CANONICAL VERSION - используется в advisors для Advise() методов
func CreateFakeToolInput(filePath, content string) *core.ToolInput {
	toolParams := ToolInputParams{
		FilePath: filePath,
		Content:  content,
	}
	toolParamsJSON, _ := json.Marshal(toolParams)

	return &core.ToolInput{
		ToolName:  "Write",
		ToolInput: json.RawMessage(toolParamsJSON),
		FilePath:  filePath,
		Content:   content,
	}
}
