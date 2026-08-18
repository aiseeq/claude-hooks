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

	if err := extractToolSpecificData(&input); err != nil {
		return nil, fmt.Errorf("failed to parse tool_input of %s: %w", input.ToolName, err)
	}

	return &input, nil
}

// extractToolSpecificData извлекает данные специфичные для каждого типа инструмента.
// Отсутствие tool_input не является ошибкой: часть хуков (например Stop)
// приходит без него; присутствующий, но нечитаемый tool_input — ошибка
func extractToolSpecificData(input *ToolInput) error {
	if len(input.ToolInput) == 0 {
		return nil
	}

	toolData, err := decodeToolInput(input.ToolInput)
	if err != nil {
		return err
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
		input.NewString = joinEditStrings(toolData["edits"])

	case "Bash":
		input.Command = stringField(toolData, "command")
	}
	return nil
}

// decodeToolInput разбирает tool_input: объект либо JSON-строка с объектом внутри
func decodeToolInput(raw json.RawMessage) (map[string]any, error) {
	var toolData map[string]any
	if err := json.Unmarshal(raw, &toolData); err == nil {
		return toolData, nil
	}

	var nested string
	if err := json.Unmarshal(raw, &nested); err != nil {
		return nil, fmt.Errorf("tool_input is neither an object nor a string: %w", err)
	}
	if err := json.Unmarshal([]byte(nested), &toolData); err != nil {
		return nil, fmt.Errorf("tool_input string does not contain a JSON object: %w", err)
	}
	return toolData, nil
}

// joinEditStrings объединяет все new_string из массива edits инструмента MultiEdit
func joinEditStrings(edits any) string {
	list, ok := edits.([]any)
	if !ok {
		return ""
	}
	var allNewStrings []string
	for _, edit := range list {
		editMap, ok := edit.(map[string]any)
		if !ok {
			continue
		}
		if newString := stringField(editMap, "new_string"); newString != "" {
			allNewStrings = append(allNewStrings, newString)
		}
	}
	return strings.Join(allNewStrings, "\n")
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
