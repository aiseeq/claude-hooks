package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

func newWriteGuard(t *testing.T) *WriteGuardTool {
	t.Helper()
	tool, err := NewWriteGuardTool(core.ToolConfig{Enabled: true}, testLogger(t))
	if err != nil {
		t.Fatalf("NewWriteGuardTool: %v", err)
	}
	tool.rm.lookupEnv = func(name string) (string, bool) {
		v, ok := rmGuardEnv[name]
		return v, ok
	}
	return tool
}

func validateWrite(t *testing.T, command string) *core.ValidationResult {
	t.Helper()
	result, err := newWriteGuard(t).ValidateTool(context.Background(),
		&core.ToolInput{ToolName: "Bash", Command: command, CWD: "/home/u/proj"})
	if err != nil {
		t.Fatalf("ValidateTool: %v", err)
	}
	return result
}

// longHeredoc запись here-doc в файл: n строк по 55 символов и строка rm в конце тела
func longHeredoc(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %03d abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ", i+1)
	}
	return "cat <<'EOF' > /home/u/sb/L0\n" + strings.Join(lines, "\n") + "\nrm -rf $NOPE/*\nEOF"
}

// TestWriteGuardMatchesClaudeCode сверяет хук с вердиктами Claude Code 2.1.305:
// каждую команду прогнали через claude -p в bypassPermissions, true — Claude Code
// остановил её запросом «Dangerous rm operation detected». Записи через Bash
// (>, >>, tee, here-doc, sed -i, ~, $VAR, вне рабочей папки, .git, .claude)
// не спрашивают; спрашивает только строка rm из тела here-doc внутри составной
// команды или в команде длиннее 10000 символов. Ни одну из этих команд не
// должен отклонять rm_guard: настоящих rm по переменной в них нет.
func TestWriteGuardMatchesClaudeCode(t *testing.T) {
	cases := []struct {
		command string
		ask     bool
	}{
		{"echo hi > w0.txt", false},
		{"echo hi >> w0.txt", false},
		{"echo hi > /home/u/sb/w1.txt", false},
		{"echo hi >> /home/u/sb/w1.txt", false},
		{"echo hi > ~/.cache/claude-tmp/claude-1000/-home-aisee/8abab4f1-9009-4f85-b79f-c72859c387ba/scratchpad/gt/sb/w2.txt", false},
		{"F=/home/u/sb/w3.txt; echo hi > $F", false},
		{"F=/home/u/sb/w3.txt; echo hi > \"$F\"", false},
		{"F=~/.cache/claude-tmp/claude-1000/-home-aisee/8abab4f1-9009-4f85-b79f-c72859c387ba/scratchpad/gt/sb/w4.txt; echo hi > $F", false},
		{"echo hi > \"$HOME/.cache/claude-tmp/claude-1000/-home-aisee/8abab4f1-9009-4f85-b79f-c72859c387ba/scratchpad/gt/sb/w5.txt\"", false},
		{"echo hi > $HOME/.cache/claude-tmp/claude-1000/-home-aisee/8abab4f1-9009-4f85-b79f-c72859c387ba/scratchpad/gt/sb/w6.txt", false},
		{"echo hi | tee w7.txt", false},
		{"echo hi | tee -a /home/u/sb/w7.txt", false},
		{"echo hi | tee ~/.cache/claude-tmp/claude-1000/-home-aisee/8abab4f1-9009-4f85-b79f-c72859c387ba/scratchpad/gt/sb/w8.txt >/dev/null", false},
		{"F=/home/u/sb/w9.txt; echo hi | tee \"$F\"", false},
		{"cat <<'EOF' > /home/u/sb/h1.txt\nhello\nEOF", false},
		{"cat > /home/u/sb/h2.txt <<EOF\nhello $HOME\nEOF", false},
		{"{ cat <<'EOF'\nhello\nEOF\n} > /home/u/sb/h3.txt", false},
		{"F=~/.cache/claude-tmp/claude-1000/-home-aisee/8abab4f1-9009-4f85-b79f-c72859c387ba/scratchpad/gt/sb/h4.txt; { cat <<'EOF'\nhello\nEOF\n} > $F; wc -l $F", false},
		{"cat <<'EOF' > /home/u/sb/r1.txt\nrm -rf $NOPE/*\nEOF", false},
		{"cat <<'EOF' > /home/u/sb/r2.txt\n{\"T=$(mktemp -d)\\nfalse && rm -rf \\\"$T\\\"/*\", true},\nEOF", false},
		{"{ cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > /home/u/sb/r3.txt", true},
		{"F=~/.cache/claude-tmp/claude-1000/-home-aisee/8abab4f1-9009-4f85-b79f-c72859c387ba/scratchpad/gt/sb/r4.txt; { cat <<'EOF'\nline\nEOF\ncat /home/u/sb/a.tmp\ncat <<'EOF'\n{`echo $(false && rm -rf $NOPE/*)`, true},\nEOF\n} > $F; wc -l $F", false},
		{"echo 'rm -rf $NOPE/*' > /home/u/sb/r5.txt", false},
		{"cat <<'EOF'\nrm -rf $NOPE/*\nEOF", false},
		{"ls > /dev/null 2>&1", false},
		{"ls /home/u/sb 2>&1 | head -1", false},
		{"ls /home/u/sb > /home/u/sb/out.log 2>&1", false},
		{"L=/home/u/sb/out2.log; ls /home/u/sb > \"$L\" 2>&1; tail -1 \"$L\"", false},
		{"mkdir -p .git && echo x > .git/w.txt", false},
		{"mkdir -p .claude && echo x > .claude/w.txt", false},
		{"mkdir -p .vscode && echo x > .vscode/w.txt", false},
		{"false && echo x > ~/.bashrc", false},
		{"false && echo x >> /etc/hosts", false},
		{"false && echo x > ~/.claude/settings.json", false},
		{"false && echo x | tee ~/.claude/w.txt", false},
		{"false && echo x > /home/aisee/.ssh/w.txt", false},
		{"cd /home/u/sb && cd .. && echo x > sb/c1.txt", false},
		{"(echo x) > /home/u/sb/c2.txt", false},
		{"echo x > $(echo /home/u/sb)/c3.txt", false},
		{"echo x > `echo /home/u/sb`/c4.txt", false},
		{"sed -i 's/hi/ho/' /home/u/sb/w1.txt", false},
		{"printf 'a\\n' > /home/u/sb/p1.txt && cat /home/u/sb/p1.txt", false},
		{"{ cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n}", true},
		{"{ cat <<EOF\nrm -rf $NOPE/*\nEOF\n} > /home/u/sb/x1", true},
		{"( cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n) > /home/u/sb/x2", true},
		{"{ cat <<'EOF'\nfoo rm -rf $NOPE/*\nEOF\n} > /home/u/sb/x3", false},
		{"{ cat <<'EOF'\n  rm -rf $NOPE/*\nEOF\n} > /home/u/sb/x4", true},
		{"{ cat <<'EOF'\n{\"cat <<EOF\\nrm -rf $NOPE/*\\nEOF\", false},\nEOF\n} > /home/u/sb/x5", false},
		{"{ cat <<'EOF'\n{\"T=$(mktemp -d)\\nfalse && rm -rf \\\"$T\\\"/*\", true},\nEOF\n} > /home/u/sb/x6", false},
		{"{ cat <<'EOF'\nrm -rf \"$T\"/*\nEOF\n} > /home/u/sb/x7", true},
		{"{ cat <<'EOF'\nrm -rf \"${T:?}\"/*\nEOF\n} > /home/u/sb/x8", false},
		{"{ echo a; cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > /home/u/sb/x9", true},
		{"if true; then cat <<'EOF'\nrm -rf $NOPE/*\nEOF\nfi > /home/u/sb/x10", true},
		{"for i in 1; do cat <<'EOF'\nrm -rf $NOPE/*\nEOF\ndone > /home/u/sb/x11", true},
		{"cat <<'EOF' | tee /home/u/sb/x12\nrm -rf $NOPE/*\nEOF", false},
		{"cat <<'EOF' > /home/u/sb/x13 && echo ok\nrm -rf $NOPE/*\nEOF", false},
		{"F=/home/u/sb/x14; { cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > $F", true},
		{"{ cat <<'EOF'\nsudo rm -rf $NOPE/*\nEOF\n} > /home/u/sb/x15", true},
		{"{ cat <<'EOF'\nrm -rf /\nEOF\n} > /home/u/sb/x16", false},
		{"{ cat <<'EOF'\nrm -rf ~\nEOF\n} > /home/u/sb/x17", false},
		{"{ cat <<'EOF'\nrm -rf $1/*\nEOF\n} > /home/u/sb/x18", true},
		{"X=$(cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n); echo \"$X\" > /home/u/sb/x19", false},
		{"false && bash <<'EOF'\nrm -rf $NOPE/*\nEOF", false},
		{"{ cat <<-'EOF'\n\trm -rf $NOPE/*\n\tEOF\n} > /home/u/sb/x21", true},
		{"{ cat <<'EOF'\nline\nEOF\ncat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > /home/u/sb/x22", true},
		{"{ cat <<'EOF'\nrm -rf $NOPE/*\nEOF\ncat <<'EOF'\nline\nEOF\n} > /home/u/sb/x23", true},
		{"F=/home/u/sb/x24; { cat <<'EOF'\nline\nEOF\ncat /home/u/sb/a.tmp\ncat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > $F", true},
		{"{ cat <<'EOF'\nrm -rf /tmp/zz-none\nEOF\n} > /home/u/sb/x25", false},
		{"{ cat <<'EOF'\nS=x; rm -rf $NOPE/*\nEOF\n} > /home/u/sb/x26", true},
		{"{ cat <<'EOF'\necho $(rm -rf $NOPE/*)\nEOF\n} > /home/u/sb/x27", false},
		{"{ cat <<'EOF'\nx `rm -rf $NOPE/*`\nEOF\n} > /home/u/sb/x28", false},
		{"{\ncat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > /home/u/sb/x29", true},
		{"{ cat <<'EOF'; }\nrm -rf $NOPE/*\nEOF", false},
		{"f() { cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n}; f > /home/u/sb/x31", true},
		{"{ cat <<'EOF'\nrm -f $D/a.zz\nEOF\n} > /home/u/sb/x32", false},
		{"{ cat <<'EOF'\nrmdir $NOPE/\nEOF\n} > /home/u/sb/x33", true},
		{"false && node -e 'const a = 1;\nrm -rf $NOPE/*'", false},
		{"X='a\nrm -rf $NOPE/*'; echo \"$X\" > /home/u/sb/q7", false},
		{"{ cat <<'EOF'\nuse `x\nrm -rf $NOPE/*\n` y\nEOF\n} > /home/u/sb/q10", false},
		{"{ cat <<'EOF'\nf $(a\nrm -rf $NOPE/*\n) z\nEOF\n} > /home/u/sb/q11", false},
		{"while read l; do echo $l; done <<'EOF' > /home/u/sb/q12\nrm -rf $NOPE/*\nEOF", false},
		{"echo start\n{ cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > /home/u/sb/q14\necho end", true},
		{"{ cat <<'EOF'\n# rm -rf $NOPE/*\nEOF\n} > /home/u/sb/q16", false},
		{"{ cat <<'EOF'\nfoo; rm -rf $NOPE/*\nEOF\n} > /home/u/sb/q17", true},
		{"{ cat <<'EOF'\nfoo && rm -rf $NOPE/*\nEOF\n} > /home/u/sb/q18", true},
		{"{ cat <<'EOF'\nfoo | rm -rf $NOPE/*\nEOF\n} > /home/u/sb/q19", true},
		{"{ cat <<'EOF'\n\"rm -rf $NOPE/*\"\nEOF\n} > /home/u/sb/q21", false},
		{"{ cat <<'EOF'\nif x; then rm -rf $NOPE/*; fi\nEOF\n} > /home/u/sb/q22", true},
		{"{ cat <<'EOF'\nS=/x rm -rf $NOPE/*\nEOF\n} > /home/u/sb/q23", true},
		{"{ cat <<'EOF'\n\trm -rf $NOPE/*\nEOF\n} > /home/u/sb/q24", true},
		{"{ cat <<'EOF'\n(rm -rf $NOPE/*)\nEOF\n} > /home/u/sb/q25", false},
		{"{ cat <<'EOF'\n{ rm -rf $NOPE/*; }\nEOF\n} > /home/u/sb/q26", true},
		{"case x in x) cat <<'EOF' ;;\nrm -rf $NOPE/*\nEOF\nesac > /home/u/sb/q27", true},
		{"! { cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > /home/u/sb/q28", true},
		{"time { cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > /home/u/sb/q29", false},
		// 180 строк — длиннее 10000 символов, 140 — короче
		{longHeredoc(180), true},
		{longHeredoc(140), false},
	}
	for _, tc := range cases {
		if got := !validateWrite(t, tc.command).IsValid; got != tc.ask {
			t.Errorf("write_guard denied=%v, Claude Code asked=%v: %q", got, tc.ask, tc.command)
		}
		if !validateRm(t, tc.command).IsValid {
			t.Errorf("rm_guard denied a command without real rm: %q", tc.command)
		}
	}
}

// TestWriteGuardReason текст отказа: строка из тела и подсказка про Write
func TestWriteGuardReason(t *testing.T) {
	cases := []struct {
		command, reason string
	}{
		{"F=x.go; { cat <<'EOF'\npackage x\nfoo && rm -rf \"$T\"/*\nEOF\n} > $F",
			"строка `rm -rf \"$T\"/*` в теле here-doc похожа на rm по переменной без защиты: here-doc внутри { … }, ( … ), if, for, while, case или функции Claude Code читает как команды. Создай файл инструментом Write, правь Edit, а в Bash оставь только команду, которая его использует"},
		{longHeredoc(180),
			"строка `rm -rf $NOPE/*` в теле here-doc похожа на rm по переменной без защиты: команду длиннее 10000 символов Claude Code не делит на команды и читает тела всех here-doc как команды. Создай файл инструментом Write, правь Edit, а в Bash оставь только команду, которая его использует"},
	}
	for _, tc := range cases {
		result := validateWrite(t, tc.command)
		if result.IsValid || len(result.Violations) != 1 {
			t.Fatalf("expected one violation for %q, got %+v", tc.command, result)
		}
		if got := result.Violations[0].Suggestion; got != tc.reason {
			t.Errorf("reason for %q:\n got %s\nwant %s", tc.command, got, tc.reason)
		}
	}
}

// TestWriteGuardDisabled выключенная проверка пропускает всё
func TestWriteGuardDisabled(t *testing.T) {
	tool, err := NewWriteGuardTool(core.ToolConfig{Enabled: false}, testLogger(t))
	if err != nil {
		t.Fatalf("NewWriteGuardTool: %v", err)
	}
	result, err := tool.ValidateTool(context.Background(),
		&core.ToolInput{ToolName: "Bash", Command: "{ cat <<'EOF'\nrm -rf $NOPE/*\nEOF\n} > x"})
	if err != nil || !result.IsValid {
		t.Fatalf("disabled write_guard: %+v, %v", result, err)
	}
}
