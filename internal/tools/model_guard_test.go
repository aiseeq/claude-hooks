package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func modelGuardForTest(t *testing.T, contents string) *ModelGuardTool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "approved")
	if contents != "\x00" {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MODELS_APPROVED", path)
	logger, err := core.NewLogger(core.DefaultLoggerConfig())
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := NewModelGuardTool(core.ToolConfig{Enabled: true}, logger)
	return tool
}

func TestModelGuard(t *testing.T) {
	tool := modelGuardForTest(t, "opus claude-opus-5-5\nsol gpt-6.1-sol\nluna gpt-6-luna\n")
	cases := []struct {
		name, tool, raw, command string
		reject                   bool
	}{
		{"Agent без модели", "Agent", `{}`, "", false},
		{"Agent имя", "Agent", `{"model":"opus"}`, "", false},
		{"Agent id", "Agent", `{"model":"claude-opus-5-5"}`, "", false},
		{"Agent sonnet", "Agent", `{"model":"sonnet"}`, "", true},
		{"Agent haiku", "Agent", `{"model":"haiku"}`, "", true},
		{"claude sonnet", "Bash", "", "claude -p --model sonnet \"x\"", true},
		{"claude approved", "Bash", "", "claude --model=opus", false},
		{"codex luna", "Bash", "", "codex exec -m gpt-6-luna", false},
		{"codex unknown", "Bash", "", "codex -m gpt-5", true},
		{"opencode provider", "Bash", "", "opencode run -m openai/gpt-6-luna", false},
		{"opencode unknown", "Bash", "", "opencode run -m deepseek/deepseek-v4", true},
		{"timeout", "Bash", "", "timeout 60 claude --model haiku", true},
		{"codex config", "Bash", "", "codex -c model=gpt-4o", true},
		{"dynamic", "Bash", "", `claude --model "$M"`, true},
		{"echo", "Bash", "", "echo 'claude --model sonnet'", false},
		{"grep", "Bash", "", "grep -- '--model sonnet' f", false},
		{"here-doc", "Bash", "", "cat <<'EOF'\nclaude --model sonnet\nEOF", false},
		{"ls", "Bash", "", "ls", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := &core.ToolInput{ToolName: tc.tool, Command: tc.command, ToolInput: []byte(tc.raw)}
			got, err := tool.ValidateTool(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if got.IsValid == tc.reject {
				t.Fatalf("valid=%v violations=%v", got.IsValid, got.Violations)
			}
		})
	}
}

func TestModelGuardApprovedListErrors(t *testing.T) {
	for _, tc := range []struct{ name, contents, want string }{{"missing", "\x00", "не найден"}, {"empty", "# models\n", "пуст"}, {"bad line", "opus claude-opus-5-5 extra\n", "строка 1"}} {
		t.Run(tc.name, func(t *testing.T) {
			tool := modelGuardForTest(t, tc.contents)
			got, err := tool.ValidateTool(context.Background(), &core.ToolInput{ToolName: "Bash", Command: "claude --model sonnet"})
			if err != nil {
				t.Fatal(err)
			}
			if got.IsValid || !strings.Contains(got.Violations[0].Message, tc.want) {
				t.Fatalf("result=%+v want %q", got, tc.want)
			}
		})
	}
}

func TestModelGuardScan(t *testing.T) {
	dir := t.TempDir()
	approved := filepath.Join(dir, "approved")
	if err := os.WriteFile(approved, []byte("opus claude-opus-5-5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MODELS_APPROVED", approved)
	transcripts := filepath.Join(dir, "projects", "demo")
	if err := os.MkdirAll(transcripts, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(transcripts, "session.jsonl")
	data := []string{
		`{"type":"assistant","timestamp":"2026-10-21T10:00:00Z","message":{"content":[{"type":"tool_use","name":"Agent","input":{"model":"sonnet"}}]}}`,
		`{"type":"assistant","timestamp":"2026-10-21T10:01:00Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"claude --model opus"}}]}}`,
		`{"type":"assistant","timestamp":"2026-10-21T10:02:00Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo 'claude --model sonnet'"}}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(data, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logger, _ := core.NewLogger(core.DefaultLoggerConfig())
	tool, _ := NewModelGuardTool(core.ToolConfig{Enabled: true}, logger)
	result, err := ScanModelTranscripts(context.Background(), filepath.Join(dir, "projects"), 7, tool)
	if err != nil {
		t.Fatal(err)
	}
	if result.Checked != 3 || result.Rejected != 1 || result.Mentions != 1 {
		t.Fatalf("scan=%+v", result)
	}
	var out strings.Builder
	WriteModelScan(&out, result)
	if !strings.Contains(out.String(), fmt.Sprintf("%d", result.Rejected)) {
		t.Fatal(out.String())
	}
}
