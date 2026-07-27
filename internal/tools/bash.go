package tools

import (
	"context"
	"fmt"
	"regexp"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// blockedPattern опасная команда: регулярное выражение и человекочитаемое описание
type blockedPattern struct {
	regexp      *regexp.Regexp
	description string
}

// defaultBlockedRules опасные команды, блокируемые по умолчанию.
// Паттерны — регулярные выражения: подстрочное сравнение давало ложные срабатывания
// ("rm -rf /tmp/build" содержит "rm -rf /") и пропускало варианты записи ("rm -fr /")
var defaultBlockedRules = []struct {
	pattern     string
	description string
}{
	{
		// Конкретные пути (rm -rf /tmp/build, rm -rf ~/project) остаются разрешёнными
		pattern:     `\brm\s+(?:-[a-zA-Z-]+\s+)*-[a-zA-Z]*[rR][a-zA-Z]*\s+(?:/|~|\$HOME|\$\{HOME\})(?:/?\*?)\s*(?:$|[;&|])`,
		description: "рекурсивное удаление корня или домашней директории",
	},
	{
		pattern:     `\brm\s+[^;&|]*--no-preserve-root`,
		description: "снятие защиты корневого каталога",
	},
	{
		pattern:     `:\(\)\s*\{\s*:\|:&\s*\};\s*:`,
		description: "fork bomb",
	},
	{
		pattern:     `\bmkfs(\.[a-z0-9]+)?\s+/dev/`,
		description: "форматирование блочного устройства",
	},
	{
		pattern:     `\bdd\s+[^;&|]*\bof=/dev/(sd|nvme|vd|hd)`,
		description: "запись напрямую в блочное устройство",
	},
	{
		pattern:     `>\s*/dev/(sd|nvme|vd|hd)[a-z0-9]*\b`,
		description: "перезапись диска через редирект",
	},
	{
		pattern:     `--headed\b`,
		description: "запуск браузера в интерактивном режиме",
	},
}

// BashTool блокирует опасные bash-команды
type BashTool struct {
	*BaseTool
	patterns []blockedPattern
}

// NewBashTool создает валидатор bash-команд
func NewBashTool(config core.ToolConfig, logger core.Logger) (*BashTool, error) {
	var patterns []blockedPattern

	if len(config.BlockedPatterns) > 0 {
		// Пользовательский список полностью заменяет встроенный
		for _, pattern := range config.BlockedPatterns {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("failed to compile blocked pattern %q: %w", pattern, err)
			}
			patterns = append(patterns, blockedPattern{regexp: compiled})
		}
	} else {
		for _, rule := range defaultBlockedRules {
			compiled, err := regexp.Compile(rule.pattern)
			if err != nil {
				return nil, fmt.Errorf("failed to compile blocked pattern %q: %w", rule.pattern, err)
			}
			patterns = append(patterns, blockedPattern{regexp: compiled, description: rule.description})
		}
	}

	return &BashTool{
		BaseTool: NewBaseTool("bash", config.Enabled, []string{"Bash"}, logger),
		patterns: patterns,
	}, nil
}

// ValidateTool проверяет bash-команду на опасные паттерны
func (t *BashTool) ValidateTool(ctx context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	if !t.IsEnabled() || input.ToolName != "Bash" || input.Command == "" {
		return &core.ValidationResult{IsValid: true}, nil
	}

	t.Logger().Debug("validating bash command", "command", input.Command)

	var violations []core.Violation
	for _, pattern := range t.patterns {
		loc := pattern.regexp.FindStringIndex(input.Command)
		if loc == nil {
			continue
		}

		matched := input.Command[loc[0]:loc[1]]
		message := fmt.Sprintf("Опасная bash-команда: %q", matched)
		if pattern.description != "" {
			message = fmt.Sprintf("Опасная bash-команда (%s): %q", pattern.description, matched)
		}

		violations = append(violations, core.Violation{
			Type:       "dangerous_bash_command",
			Message:    message,
			Suggestion: "Укажи конкретный путь вместо корневого или убери опасный флаг",
			Severity:   core.LevelCritical,
			Line:       1,
			Column:     loc[0] + 1,
		})
	}

	return &core.ValidationResult{
		IsValid:    len(violations) == 0,
		Violations: violations,
	}, nil
}
