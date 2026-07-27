package core

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseToolInput парсит JSON входные данные от Claude Code
func ParseToolInput(data []byte) (*ToolInput, error) {
	var input ToolInput
	if err := json.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("failed to parse tool input: %w", err)
	}

	extractToolSpecificData(&input)

	return &input, nil
}

// extractToolSpecificData извлекает данные специфичные для каждого типа инструмента.
// Отсутствие или нераспознанный формат tool_input не является ошибкой: часть хуков
// (например Stop) приходит без него
func extractToolSpecificData(input *ToolInput) {
	if len(input.ToolInput) == 0 {
		return
	}

	toolData := decodeToolInput(input.ToolInput)
	if toolData == nil {
		return
	}

	switch input.ToolName {
	case "Write":
		input.FilePath = stringField(toolData, "file_path")
		input.Content = stringField(toolData, "content")

	case "Edit":
		input.FilePath = stringField(toolData, "file_path")
		input.NewString = stringField(toolData, "new_string")

	case "MultiEdit":
		input.FilePath = stringField(toolData, "file_path")
		// Для MultiEdit объединяем все new_string из массива edits
		if edits, ok := toolData["edits"].([]any); ok {
			var allNewStrings []string
			for _, edit := range edits {
				if editMap, ok := edit.(map[string]any); ok {
					if newString := stringField(editMap, "new_string"); newString != "" {
						allNewStrings = append(allNewStrings, newString)
					}
				}
			}
			input.NewString = strings.Join(allNewStrings, "\n")
		}

	case "Bash":
		input.Command = stringField(toolData, "command")
	}
}

// decodeToolInput разбирает tool_input: объект либо JSON-строка с объектом внутри
func decodeToolInput(raw json.RawMessage) map[string]any {
	var toolData map[string]any
	if err := json.Unmarshal(raw, &toolData); err == nil {
		return toolData
	}

	var nested string
	if err := json.Unmarshal(raw, &nested); err != nil {
		return nil
	}
	if err := json.Unmarshal([]byte(nested), &toolData); err != nil {
		return nil
	}
	return toolData
}

// stringField извлекает строковое поле из распарсенного tool_input
func stringField(data map[string]any, key string) string {
	value, ok := data[key].(string)
	if !ok {
		return ""
	}
	return value
}

// CreateFileAnalysis создает анализ файла из ToolInput
func CreateFileAnalysis(input *ToolInput) *FileAnalysis {
	if input.FilePath == "" {
		return nil
	}

	content := input.Content
	if content == "" {
		content = input.NewString
	}

	return &FileAnalysis{
		Path:       input.FilePath,
		Content:    content,
		Extension:  GetFileExtension(input.FilePath),
		IsTestFile: IsTestFile(input.FilePath),
		IsDocsFile: IsDocumentationFile(input.FilePath),
	}
}
