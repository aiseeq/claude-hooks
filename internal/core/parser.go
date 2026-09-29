package core

import (
	"encoding/json"
	"fmt"
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

// extractToolSpecificData извлекает данные, которые нужны проверкам.
// Отсутствие tool_input не является ошибкой: события сессии (например Stop)
// приходят без него; присутствующий, но нечитаемый tool_input — ошибка
func extractToolSpecificData(input *ToolInput) error {
	if len(input.ToolInput) == 0 || input.ToolName != "Bash" {
		return nil
	}

	toolData, err := decodeToolInput(input.ToolInput)
	if err != nil {
		return err
	}

	input.Command = stringField(toolData, "command")
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

// stringField извлекает строковое поле из распарсенного tool_input
func stringField(data map[string]any, key string) string {
	value, ok := data[key].(string)
	if !ok {
		return ""
	}
	return value
}
