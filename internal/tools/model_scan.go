package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aiseeq/claude-hooks/internal/core"
)

type modelScanToolUse struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}
type modelScanMessage struct {
	Content []modelScanToolUse `json:"content"`
}
type modelScanLine struct {
	Type      string           `json:"type"`
	Timestamp string           `json:"timestamp"`
	Message   modelScanMessage `json:"message"`
}

type modelScanExample struct{ file, timestamp, command string }
type modelScanBucket struct {
	count    int
	projects map[string]int
	examples []modelScanExample
}
type ModelScanResult struct {
	Checked         int
	Rejected        int
	ByModel         map[string]*modelScanBucket
	Mentions        int
	MentionExamples []modelScanExample
}

var (
	longModelMentionPattern  = regexp.MustCompile(`(?i)--model(?:\s*=\s*|\s+)["']?([^\s"';&|]+)`)
	shortModelMentionPattern = regexp.MustCompile(`(?i)(?:^|[\s;&|(])(?:claude|codex|opencode)\s[^\n;&|]{0,160}?\s-m\s+["']?([^\s"';&|]+)`)
)

// ScanModelTranscripts потоково проверяет вызовы инструментов в jsonl-транскриптах.
func ScanModelTranscripts(ctx context.Context, root string, days int, validator *ModelGuardTool) (*ModelScanResult, error) {
	if days < 0 {
		return nil, fmt.Errorf("число дней не может быть отрицательным")
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	r := &ModelScanResult{ByModel: map[string]*modelScanBucket{}}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}
		return scanModelFile(ctx, root, path, validator, r)
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func scanModelFile(ctx context.Context, root, path string, validator *ModelGuardTool, result *ModelScanResult) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open transcript %s: %w", path, err)
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 64*1024)
	project, err := transcriptProject(root, path)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			var event modelScanLine
			if err := json.Unmarshal([]byte(line), &event); err == nil && event.Type == "assistant" {
				if err := scanModelEvent(ctx, event, path, project, validator, result); err != nil {
					return err
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read transcript %s: %w", path, readErr)
		}
	}
}

func scanModelEvent(ctx context.Context, event modelScanLine, path, project string, validator *ModelGuardTool, result *ModelScanResult) error {
	for _, use := range event.Message.Content {
		if use.Type != "tool_use" || (use.Name != "Bash" && use.Name != "Agent") {
			continue
		}
		if err := recordModelUse(ctx, use, event.Timestamp, path, project, validator, result); err != nil {
			return err
		}
	}
	return nil
}

func recordModelUse(ctx context.Context, use modelScanToolUse, timestamp, path, project string, validator *ModelGuardTool, result *ModelScanResult) error {
	input := &core.ToolInput{ToolName: use.Name, ToolInput: use.Input}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(use.Input, &fields); err != nil {
		return fmt.Errorf("decode %s input in %s: %w", use.Name, path, err)
	}
	if raw := fields["command"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &input.Command); err != nil {
			return fmt.Errorf("decode Bash command in %s: %w", path, err)
		}
	}
	result.Checked++
	validation, err := validator.ValidateTool(ctx, input)
	if err != nil {
		return fmt.Errorf("validate transcript call in %s: %w", path, err)
	}
	if validation != nil && !validation.IsValid {
		recordRejectedModel(input, timestamp, path, project, validator, result)
	} else if use.Name == "Bash" {
		recordModelMentions(input.Command, timestamp, path, validator, result)
	}
	return nil
}

func recordRejectedModel(input *core.ToolInput, timestamp, path, project string, validator *ModelGuardTool, result *ModelScanResult) {
	modelName := reportedModel(input, validator)
	bucket := result.ByModel[modelName]
	if bucket == nil {
		bucket = &modelScanBucket{projects: map[string]int{}}
		result.ByModel[modelName] = bucket
	}
	bucket.count++
	bucket.projects[project]++
	if len(bucket.examples) < 3 {
		bucket.examples = append(bucket.examples, modelScanExample{path, timestamp, firstCommand(input.Command)})
	}
	result.Rejected++
}

func recordModelMentions(command, timestamp, path string, validator *ModelGuardTool, result *ModelScanResult) {
	for _, m := range unguardedModelMentions(command, validator.approvedPath()) {
		result.Mentions++
		if len(result.MentionExamples) < 5 {
			result.MentionExamples = append(result.MentionExamples, modelScanExample{path, timestamp, firstCommand(command) + " [" + m + "]"})
		}
	}
}

func reportedModel(input *core.ToolInput, validator *ModelGuardTool) string {
	candidates := modelCandidates(input, validator.Logger())
	for _, c := range candidates {
		if c.dynamic {
			return "<переменная>"
		}
		if !isApproved(c.value, mustReadModels(validator.approvedPath())) {
			return normalizeModel(c.value)
		}
	}
	return "<ошибка списка>"
}
func mustReadModels(path string) []approvedModel { m, _ := readApprovedModels(path); return m }
func unguardedModelMentions(command, path string) []string {
	models, errText := readApprovedModels(path)
	if errText != "" {
		return nil
	}
	var out []string
	for _, pattern := range []*regexp.Regexp{longModelMentionPattern, shortModelMentionPattern} {
		for _, match := range pattern.FindAllStringSubmatch(command, -1) {
			candidate := normalizeModel(match[1])
			if !isApproved(candidate, models) {
				out = append(out, candidate)
			}
		}
	}
	return out
}
func firstCommand(command string) string {
	command = strings.TrimSpace(command)
	r := []rune(command)
	if len(r) > 160 {
		return string(r[:160])
	}
	return command
}

// WriteModelScan печатает сводку и примеры стабильным порядком.
func WriteModelScan(w io.Writer, result *ModelScanResult) {
	fmt.Fprintf(w, "Проверено вызовов: %d\nОтклонено бы: %d\n", result.Checked, result.Rejected)
	models := make([]string, 0, len(result.ByModel))
	for m := range result.ByModel {
		models = append(models, m)
	}
	sort.Strings(models)
	fmt.Fprintln(w, "По моделям и проектам:")
	for _, m := range models {
		b := result.ByModel[m]
		fmt.Fprintf(w, "  %s: %d; проекты:", m, b.count)
		projects := make([]string, 0, len(b.projects))
		for p := range b.projects {
			projects = append(projects, p)
		}
		sort.Strings(projects)
		for _, p := range projects {
			fmt.Fprintf(w, " %s=%d", p, b.projects[p])
		}
		fmt.Fprint(w, "; примеры:")
		for _, e := range b.examples {
			fmt.Fprintf(w, " [%s | %s | %s]", e.file, e.timestamp, e.command)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Упоминания без вызова: %d\n", result.Mentions)
	if len(result.MentionExamples) > 0 {
		fmt.Fprint(w, "  примеры:")
	}
	for _, e := range result.MentionExamples {
		fmt.Fprintf(w, " [%s | %s | %s]", e.file, e.timestamp, e.command)
	}
	if len(result.MentionExamples) > 0 {
		fmt.Fprintln(w)
	}
}

// transcriptProject имя папки проекта: первый каталог под корнем транскриптов,
// у суб-агентов (<проект>/<сессия>/subagents/x.jsonl) тоже он, а не id сессии
func transcriptProject(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("transcript %s is not under %s: %w", path, root, err)
	}
	return strings.SplitN(filepath.ToSlash(rel), "/", 2)[0], nil
}
