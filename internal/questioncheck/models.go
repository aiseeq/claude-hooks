package questioncheck

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ApprovedModels читает ~/.config/models/approved: строки «<имя> <id>»,
// комментарии с «#». Судья — только модель из этого списка (правило
// пользователя 21.10: модели без его одобрения не использовать нигде)
func ApprovedModels(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open approved models: %w", err)
	}
	defer file.Close()
	models := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("approved models: malformed line %q", line)
		}
		models[fields[0]] = fields[1]
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read approved models: %w", err)
	}
	return models, nil
}

// NewCLIJudge собирает судью по имени модели, сверяя его со списком одобренных
func NewCLIJudge(approvedPath, name, effort, dir string) (CLIJudge, error) {
	models, err := ApprovedModels(approvedPath)
	if err != nil {
		return CLIJudge{}, err
	}
	id, ok := models[name]
	if !ok {
		return CLIJudge{}, fmt.Errorf("model %q is not in %s", name, approvedPath)
	}
	switch effort {
	case "low", "medium", "high":
	default:
		return CLIJudge{}, fmt.Errorf("unknown effort %q", effort)
	}
	return CLIJudge{Name: name, ID: id, Effort: effort, Dir: dir}, nil
}
