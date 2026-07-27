package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newFormatterTool(t *testing.T, config core.ToolConfig) *FormatterTool {
	t.Helper()

	tool, err := NewFormatterTool(config, core.NewTestLogger())
	if err != nil {
		t.Fatalf("failed to create tool: %v", err)
	}
	return tool
}

func TestFormatterTool_OnlyRunsInPostPhase(t *testing.T) {
	tool := newFormatterTool(t, core.ToolConfig{Enabled: true, GoFormat: true})

	input := &core.ToolInput{ToolName: "Write", FilePath: "service.go"}

	result, err := tool.ValidateTool(core.WithPhase(context.Background(), core.PhasePre), input)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if len(result.Suggestions) > 0 || len(result.Violations) > 0 {
		t.Error("на этапе pre форматирование не выполняется")
	}
}

func TestFormatterTool_FormatsGoFile(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt недоступен")
	}

	filePath := filepath.Join(t.TempDir(), "service.go")
	unformatted := "package service\nfunc Value() int {\nreturn 42\n}\n"
	if err := os.WriteFile(filePath, []byte(unformatted), 0o644); err != nil {
		t.Fatalf("не удалось создать файл: %v", err)
	}

	tool := newFormatterTool(t, core.ToolConfig{Enabled: true, GoFormat: true})

	result, err := tool.ValidateTool(
		core.WithPhase(context.Background(), core.PhasePost),
		&core.ToolInput{ToolName: "Write", FilePath: filePath},
	)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if len(result.Violations) > 0 {
		t.Fatalf("форматирование корректного файла не должно давать нарушений: %+v", result.Violations)
	}

	formatted, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("не удалось прочитать файл: %v", err)
	}
	if string(formatted) == unformatted {
		t.Error("файл должен быть отформатирован")
	}
}

func TestFormatterTool_ReportsBrokenSyntax(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt недоступен")
	}

	filePath := filepath.Join(t.TempDir(), "broken.go")
	if err := os.WriteFile(filePath, []byte("package service\nfunc ("), 0o644); err != nil {
		t.Fatalf("не удалось создать файл: %v", err)
	}

	tool := newFormatterTool(t, core.ToolConfig{Enabled: true, GoFormat: true})

	result, err := tool.ValidateTool(
		core.WithPhase(context.Background(), core.PhasePost),
		&core.ToolInput{ToolName: "Write", FilePath: filePath},
	)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if len(result.Violations) != 1 {
		t.Fatalf("ожидалось одно предупреждение, получено %d", len(result.Violations))
	}
	if result.Violations[0].Severity != core.LevelWarning {
		t.Errorf("ошибка форматирования не должна быть критической, получено %s", result.Violations[0].Severity)
	}
	// Файл уже записан на диск — блокировать нечего
	if !result.IsValid {
		t.Error("форматтер не должен блокировать операцию")
	}
}

func TestFormatterTool_SkipsUnsupportedFiles(t *testing.T) {
	tool := newFormatterTool(t, core.ToolConfig{Enabled: true, GoFormat: true, TSFormat: false})

	for _, path := range []string{"README.md", "notes.txt", "config.yaml", "app.ts"} {
		t.Run(path, func(t *testing.T) {
			result, err := tool.ValidateTool(
				core.WithPhase(context.Background(), core.PhasePost),
				&core.ToolInput{ToolName: "Write", FilePath: path},
			)
			if err != nil {
				t.Fatalf("validation failed: %v", err)
			}
			if len(result.Suggestions) > 0 || len(result.Violations) > 0 {
				t.Errorf("файл %s не должен обрабатываться", path)
			}
		})
	}
}

func TestFormatterTool_Disabled(t *testing.T) {
	tool := newFormatterTool(t, core.ToolConfig{Enabled: false, GoFormat: true})

	result, err := tool.ValidateTool(
		core.WithPhase(context.Background(), core.PhasePost),
		&core.ToolInput{ToolName: "Write", FilePath: "service.go"},
	)
	if err != nil {
		t.Fatalf("validation failed: %v", err)
	}
	if len(result.Suggestions) > 0 {
		t.Error("выключенный инструмент не должен ничего делать")
	}
}
