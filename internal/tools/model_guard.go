package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

const defaultApprovedModelsPath = "~/.config/models/approved"

const modelGuardHint = `Правило ~/.claude/CLAUDE.md "Модели"; Luna и Sol — через worker; новая модель — только через бенчмарк llmark и одобрение пользователя`

type approvedModel struct{ name, id string }

// ModelGuardTool пропускает только модели, явно перечисленные пользователем.
type ModelGuardTool struct {
	*BaseTool
	path string
}

// NewModelGuardTool создаёт проверку выбранной модели.
func NewModelGuardTool(config core.ToolConfig, logger core.Logger) (*ModelGuardTool, error) {
	return &ModelGuardTool{BaseTool: NewBaseTool("model_guard", config.Enabled, []string{"Bash", "Agent"}, logger)}, nil
}

// ValidateTool проверяет модель лишь у вызовов, которые её явно задают.
func (t *ModelGuardTool) ValidateTool(_ context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	if !t.IsEnabled() || input == nil {
		return &core.ValidationResult{IsValid: true}, nil
	}
	candidates := modelCandidates(input, t.Logger())
	if len(candidates) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}
	path := t.approvedPath()
	models, reason := readApprovedModels(path)
	if reason != "" {
		return t.reject(reason, path), nil
	}
	var reasons []string
	for _, c := range candidates {
		if c.dynamic {
			reasons = append(reasons, "модель задана переменной, назови её литералом")
		} else if !isApproved(c.value, models) {
			reasons = append(reasons, fmt.Sprintf("модель %q не одобрена", c.value))
		}
	}
	if len(reasons) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}
	return t.reject(strings.Join(reasons, "; "), path, models), nil
}

type modelCandidate struct {
	value   string
	dynamic bool
}

func modelCandidates(input *core.ToolInput, logger core.Logger) []modelCandidate {
	if input.ToolName == "Agent" {
		var found []modelCandidate
		var fields map[string]json.RawMessage
		if json.Unmarshal(input.ToolInput, &fields) == nil {
			if raw := fields["model"]; len(raw) > 0 && string(raw) != "null" {
				var model string
				if err := json.Unmarshal(raw, &model); err != nil {
					logger.Warn("поле модели Agent имеет неверный тип", "value", string(raw), "error", err)
					found = append(found, modelCandidate{value: string(raw)})
				} else if strings.TrimSpace(model) != "" {
					found = append(found, modelCandidate{value: model})
				}
			}
		}
		return found
	}
	if input.ToolName != "Bash" {
		return nil
	}
	return bashModelCandidates(input.Command)
}

func bashModelCandidates(command string) []modelCandidate {
	var found []modelCandidate
	for _, words := range splitSimpleCommands(withoutHereDocBodies(command)) {
		name, args, _ := unwrapCommand(words)
		if name != "claude" && name != "codex" && name != "opencode" {
			continue
		}
		found = append(found, candidatesFromArgs(name, args)...)
	}
	return found
}

func withoutHereDocBodies(command string) string {
	var output strings.Builder
	type delimiter struct {
		name     string
		stripTab bool
	}
	var delimiters []delimiter
	for _, line := range strings.SplitAfter(command, "\n") {
		plain := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if len(delimiters) > 0 {
			bodyLine := plain
			if delimiters[0].stripTab {
				bodyLine = strings.TrimLeft(bodyLine, "\t")
			}
			if bodyLine == delimiters[0].name {
				delimiters = delimiters[1:]
			}
			continue
		}
		output.WriteString(line)
		for _, words := range splitSimpleCommands(plain) {
			for i := 0; i < len(words); i++ {
				value := words[i].text
				if after, ok := strings.CutPrefix(value, "<<-"); ok {
					delimiters = append(delimiters, delimiter{name: after, stripTab: true})
				} else if strings.HasPrefix(value, "<<") && len(value) > 2 {
					delimiters = append(delimiters, delimiter{name: strings.TrimPrefix(value, "<<")})
				} else if value == "<<" && i+1 < len(words) {
					i++
					delimiters = append(delimiters, delimiter{name: words[i].text})
				}
			}
		}
	}
	return output.String()
}

func candidatesFromArgs(name string, args []word) []modelCandidate {
	var found []modelCandidate
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a.text == "--model" || (name != "claude" && a.text == "-m"):
			if i+1 < len(args) {
				i++
				found = append(found, modelCandidate{value: args[i].text, dynamic: !args[i].literal})
			}
		case strings.HasPrefix(a.text, "--model="):
			found = append(found, modelCandidate{value: strings.TrimPrefix(a.text, "--model="), dynamic: !a.literal})
		case name == "codex" && (a.text == "-c" || a.text == "--config"):
			if i+1 < len(args) {
				i++
				if candidate, ok := codexConfigCandidate(args[i]); ok {
					found = append(found, candidate)
				}
			}
		}
	}
	return found
}

func codexConfigCandidate(config word) (modelCandidate, bool) {
	if !strings.HasPrefix(config.text, "model=") {
		return modelCandidate{}, false
	}
	return modelCandidate{value: strings.TrimPrefix(config.text, "model="), dynamic: !config.literal}, true
}

func (t *ModelGuardTool) approvedPath() string {
	if p := os.Getenv("MODELS_APPROVED"); p != "" {
		return p
	}
	if t.path != "" {
		return t.path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Logger().Warn("не удалось определить домашний каталог для списка моделей", "error", err)
		return defaultApprovedModelsPath
	}
	return filepath.Join(home, ".config", "models", "approved")
}

func readApprovedModels(path string) ([]approvedModel, string) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Sprintf("список одобренных моделей %s не найден", path)
		}
		return nil, fmt.Sprintf("список одобренных моделей %s не читается: %v", path, err)
	}
	defer f.Close()
	// Список короткий, но читаем строками с увеличенным пределом Scanner.
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 1024*1024)
	var models []approvedModel
	for line := 1; s.Scan(); line++ {
		text := strings.TrimSpace(strings.SplitN(s.Text(), "#", 2)[0])
		if text == "" {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 {
			return nil, fmt.Sprintf("список одобренных моделей %s, строка %d: ожидаются ровно два поля", path, line)
		}
		models = append(models, approvedModel{fields[0], fields[1]})
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Sprintf("список одобренных моделей %s не читается: %v", path, err)
	}
	if len(models) == 0 {
		return nil, fmt.Sprintf("список одобренных моделей %s пуст", path)
	}
	return models, ""
}

func isApproved(candidate string, models []approvedModel) bool {
	candidate = normalizeModel(candidate)
	for _, m := range models {
		if strings.EqualFold(candidate, m.name) || strings.EqualFold(candidate, m.id) {
			return true
		}
	}
	return false
}

func normalizeModel(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	if i := strings.Index(model, "["); i >= 0 && strings.HasSuffix(model, "]") {
		model = model[:i]
	}
	return model
}

func (t *ModelGuardTool) reject(reason, path string, models ...[]approvedModel) *core.ValidationResult {
	var list []string
	if len(models) > 0 {
		for _, m := range models[0] {
			list = append(list, fmt.Sprintf("%s (%s)", m.name, m.id))
		}
	}
	message := reason
	if len(list) > 0 {
		message += "; одобрены: " + strings.Join(list, ", ")
	}
	message += "; " + modelGuardHint
	v := core.NewViolation("model_guard", message, "", core.LevelCritical, 0, 0)
	return core.NewValidationResult(false, []core.Violation{v}, []string{message})
}
