package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// protectedComms процессы, без которых падает сессия пользователя целиком:
// init и менеджеры служб, шина, графическая оболочка, звук, вход по ssh
var protectedComms = map[string]bool{
	"systemd": true, "init": true, "systemd-logind": true,
	"dbus-daemon": true, "dbus-broker": true, "sshd": true, "sshd-session": true,
	"Xwayland": true, "Xorg": true, "kwin_wayland": true, "kwin_x11": true,
	"plasmashell": true, "gnome-shell": true, "sway": true, "Hyprland": true,
	"sddm": true, "gdm": true, "pipewire": true, "wireplumber": true,
}

// KillGuardTool блокирует kill, pkill и killall, цель которых нельзя
// проверить до запуска или которые заденут саму сессию: PID 1, systemd,
// предков хука (claude, терминал), оболочку, исполняющую команду
type KillGuardTool struct {
	*BaseTool
	procRoot string
	selfPID  int
}

// NewKillGuardTool создает защиту от убийства сессии
func NewKillGuardTool(config core.ToolConfig, logger core.Logger) (*KillGuardTool, error) {
	return &KillGuardTool{
		BaseTool: NewBaseTool("kill_guard", config.Enabled, []string{"Bash"}, logger),
		procRoot: "/proc",
		selfPID:  os.Getpid(),
	}, nil
}

const killGuardHint = "Найди PID отдельной командой (ps -o pid,ppid,comm,args -p … или pgrep -a '[x]yz'), посмотри на вывод и убей литералом: kill 12345"

// ValidateTool проверяет команды, посылающие сигналы процессам
func (t *KillGuardTool) ValidateTool(_ context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	if !t.IsEnabled() || input.ToolName != "Bash" || !strings.Contains(input.Command, "kill") {
		return &core.ValidationResult{IsValid: true}, nil
	}

	var reasons []string
	var procs []procInfo
	for _, cmd := range splitSimpleCommands(input.Command) {
		name, args, viaXargs := unwrapCommand(cmd)
		if name != "kill" && name != "pkill" && name != "killall" {
			continue
		}
		if procs == nil {
			var err error
			if procs, err = t.listProcs(); err != nil {
				return nil, err
			}
		}
		var reason string
		switch {
		case viaXargs:
			reason = name + " через xargs: цели приходят из вывода другой команды, проверить их до запуска нельзя"
		case name == "kill":
			reason = t.checkKill(args, procs)
		default:
			reason = t.checkPattern(name, args, input.Command, procs)
		}
		if reason != "" {
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}

	violations := make([]core.Violation, 0, len(reasons))
	for _, r := range reasons {
		violations = append(violations, core.NewViolation("kill_guard",
			"Сигнал может убить не тот процесс или саму сессию:", r, core.LevelCritical, 0, 0))
	}
	return core.NewValidationResult(false, violations, append(reasons, killGuardHint)), nil
}

// checkKill проверяет цели kill: только литеральные PID и job spec, ни один
// не задевает сессию
func (t *KillGuardTool) checkKill(args []word, procs []procInfo) string {
	targets := args
	if len(targets) > 0 && strings.HasPrefix(targets[0].text, "-") && targets[0].literal {
		switch targets[0].text {
		case "-l", "-L", "--list", "--table":
			return ""
		case "-s", "-n", "--signal":
			targets = targets[min(2, len(targets)):]
		default:
			targets = targets[1:]
		}
	}
	if len(targets) > 0 && targets[0].text == "--" {
		targets = targets[1:]
	}
	ancestors := t.ancestors(procs)
	for _, w := range targets {
		if !w.literal {
			return fmt.Sprintf("kill %s: PID вычисляется при запуске, проверить его заранее нельзя", w.text)
		}
		if strings.HasPrefix(w.text, "%") {
			continue
		}
		pid, err := strconv.Atoi(w.text)
		if err != nil {
			return fmt.Sprintf("kill %s: цель не PID (%v)", w.text, err)
		}
		if reason := t.dangerous(pid, procs, ancestors); reason != "" {
			return fmt.Sprintf("kill %d: %s", pid, reason)
		}
	}
	return ""
}

// patternArgs разобранные аргументы pkill или killall
type patternArgs struct {
	full, exact, isRegex bool
	patterns             []string
}

// parsePatternArgs разбирает флаги и шаблоны; непустая строка — причина отказа
func parsePatternArgs(name string, args []word) (patternArgs, string) {
	pa := patternArgs{isRegex: name == "pkill"}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.literal {
			return pa, fmt.Sprintf("%s %s: шаблон вычисляется при запуске, проверить его заранее нельзя", name, a.text)
		}
		switch {
		case a.text == "-f" || a.text == "--full":
			pa.full = true
		case a.text == "-x" || a.text == "--exact" || a.text == "-e":
			pa.exact = true
		case a.text == "-r" || a.text == "--regexp":
			pa.isRegex = true
		case optionTakesValue(name, a.text):
			i++
		case strings.HasPrefix(a.text, "-"):
			// сигнал или флаг без значения
		default:
			pa.patterns = append(pa.patterns, a.text)
		}
	}
	if name == "pkill" && len(pa.patterns) > 1 {
		pa.patterns = pa.patterns[len(pa.patterns)-1:]
	}
	return pa, ""
}

// regexp шаблон в том виде, как его сопоставляет утилита: killall без -r
// сравнивает имя целиком, pkill -x — тоже
func (pa patternArgs) regexp(p string) (*regexp.Regexp, error) {
	expr, exact := p, pa.exact
	if !pa.isRegex {
		expr, exact = regexp.QuoteMeta(p), true
	}
	if exact {
		expr = "^(?:" + expr + ")$"
	}
	return regexp.Compile(expr)
}

// checkPattern прикидывает, что заденет pkill или killall сейчас: шаблон
// сверяется с теми же полями процессов, что у самой утилиты
func (t *KillGuardTool) checkPattern(name string, args []word, command string, procs []procInfo) string {
	pa, reason := parsePatternArgs(name, args)
	if reason != "" {
		return reason
	}
	ancestors := t.ancestors(procs)
	for _, p := range pa.patterns {
		re, err := pa.regexp(p)
		if err != nil {
			// утилита сама откажется от такого шаблона
			t.Logger().Debug("kill pattern does not compile", "pattern", p, "error", err)
			continue
		}
		if pa.full && re.MatchString(command) {
			return fmt.Sprintf("%s -f %q совпадает с текстом самой команды и убьёт оболочку, которая её исполняет; шаблон вида '[x]yz' себя не ловит", name, p)
		}
		for _, pr := range procs {
			subject := pr.comm
			if pa.full {
				subject = pr.cmdline
			}
			if !re.MatchString(subject) {
				continue
			}
			if reason := t.dangerous(pr.pid, procs, ancestors); reason != "" {
				return fmt.Sprintf("%s %q задевает PID %d: %s", name, p, pr.pid, reason)
			}
		}
	}
	return ""
}

// dangerous возвращает причину, по которой процесс трогать нельзя
func (t *KillGuardTool) dangerous(pid int, procs []procInfo, ancestors map[int]bool) string {
	if pid <= 1 {
		return "PID ≤ 1 — это init или все процессы пользователя (группа процессов)"
	}
	if pid == t.selfPID || ancestors[pid] {
		return "предок этой сессии Claude (claude, оболочка, терминал или менеджер сессии)"
	}
	for _, p := range procs {
		if p.pid == pid && protectedComms[p.comm] {
			return fmt.Sprintf("%s — на нём держится сессия пользователя", p.comm)
		}
	}
	return ""
}

// procInfo процесс по /proc
type procInfo struct {
	pid, ppid     int
	comm, cmdline string
}

// listProcs читает все процессы из procRoot. Процесс, завершившийся между
// ReadDir и чтением его файлов, пропускается
func (t *KillGuardTool) listProcs() ([]procInfo, error) {
	entries, err := os.ReadDir(t.procRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", t.procRoot, err)
	}
	procs := make([]procInfo, 0, len(entries))
	for _, e := range entries {
		if name := e.Name(); name[0] < '0' || name[0] > '9' {
			continue // не процесс: self, sys, meminfo
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			t.Logger().Debug("proc entry skipped", "name", e.Name(), "error", err)
			continue
		}
		p, err := readProc(filepath.Join(t.procRoot, e.Name()), pid)
		if err != nil {
			t.Logger().Debug("process skipped", "pid", pid, "error", err)
			continue
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// readProc читает stat и cmdline одного процесса
func readProc(dir string, pid int) (procInfo, error) {
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return procInfo{}, fmt.Errorf("read stat: %w", err)
	}
	// comm в stat может содержать пробелы и скобки: поля после последней ')'
	s := string(stat)
	start, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if start < 0 || end < start {
		return procInfo{}, fmt.Errorf("stat без comm: %q", s)
	}
	fields := strings.Fields(s[end+1:])
	if len(fields) < 2 {
		return procInfo{}, fmt.Errorf("stat без ppid: %q", s)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procInfo{}, fmt.Errorf("ppid: %w", err)
	}
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return procInfo{}, fmt.Errorf("read cmdline: %w", err)
	}
	return procInfo{
		pid:     pid,
		ppid:    ppid,
		comm:    s[start+1 : end],
		cmdline: strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " ")),
	}, nil
}

// ancestors цепочка родителей хука до init
func (t *KillGuardTool) ancestors(procs []procInfo) map[int]bool {
	parent := make(map[int]int, len(procs))
	for _, p := range procs {
		parent[p.pid] = p.ppid
	}
	out := map[int]bool{}
	for pid := parent[t.selfPID]; pid > 1 && !out[pid]; pid = parent[pid] {
		out[pid] = true
	}
	return out
}

// word слово оболочки без кавычек; literal — в нём нет подстановок $ и `
type word struct {
	text    string
	literal bool
}

// shellLexer грубый разбор оболочки: слова и простые команды
type shellLexer struct {
	cmds    [][]word
	cur     []word
	buf     strings.Builder
	literal bool
	inWord  bool
	quote   rune
}

func (l *shellLexer) flushWord() {
	if l.inWord {
		l.cur = append(l.cur, word{text: l.buf.String(), literal: l.literal})
	}
	l.buf.Reset()
	l.literal, l.inWord = true, false
}

func (l *shellLexer) flushCmd() {
	l.flushWord()
	if len(l.cur) > 0 {
		l.cmds = append(l.cmds, l.cur)
	}
	l.cur = nil
}

// add добавляет символ слова; $ и ` делают слово вычисляемым
func (l *shellLexer) add(r rune) {
	if r == '$' || r == '`' {
		l.literal = false
	}
	l.buf.WriteRune(r)
	l.inWord = true
}

// quoted обрабатывает символ внутри кавычек
func (l *shellLexer) quoted(r rune) {
	switch {
	case r == l.quote:
		l.quote = 0
	case l.quote == '\'':
		l.buf.WriteRune(r)
	default:
		l.add(r)
	}
}

// splitSimpleCommands режет команду на простые по ; & | ( ) и переводу строки
// вне кавычек. Разбор грубый, но для поиска kill и его аргументов достаточен
func splitSimpleCommands(command string) [][]word {
	l := &shellLexer{literal: true}
	for _, r := range command {
		switch {
		case l.quote != 0:
			l.quoted(r)
		case r == '\'' || r == '"':
			l.quote, l.inWord = r, true
		case strings.ContainsRune(";&|\n()", r):
			l.flushCmd()
		case r == ' ' || r == '\t':
			l.flushWord()
		default:
			l.add(r)
		}
	}
	l.flushCmd()
	return l.cmds
}

// unwrapCommand снимает обёртки sudo, timeout, nohup, env, xargs и возвращает
// имя утилиты и её аргументы
func unwrapCommand(cmd []word) (name string, args []word, viaXargs bool) {
	for len(cmd) > 0 {
		head := filepath.Base(cmd[0].text)
		cmd = cmd[1:]
		switch head {
		case "sudo", "nohup", "env", "exec", "command", "nice", "setsid":
			for len(cmd) > 0 && (strings.HasPrefix(cmd[0].text, "-") || strings.Contains(cmd[0].text, "=")) {
				cmd = cmd[1:]
			}
		case "timeout":
			for len(cmd) > 0 && strings.HasPrefix(cmd[0].text, "-") {
				cmd = cmd[1:]
			}
			if len(cmd) > 0 {
				cmd = cmd[1:] // длительность
			}
		case "xargs":
			viaXargs = true
			for len(cmd) > 0 && strings.HasPrefix(cmd[0].text, "-") {
				cmd = cmd[1:]
			}
		default:
			return head, cmd, viaXargs
		}
	}
	return "", nil, viaXargs
}

// optionTakesValue флаги pkill и killall, за которыми идёт отдельное значение
func optionTakesValue(name, opt string) bool {
	if name == "pkill" {
		switch opt {
		case "-u", "-U", "-g", "-G", "-P", "-s", "-t", "--signal", "--euid", "--uid",
			"--pgroup", "--group", "--parent", "--session", "--terminal", "--ns", "--nslist":
			return true
		}
		return false
	}
	switch opt {
	case "-u", "--user", "-s", "--signal", "-o", "--older-than", "-y", "--younger-than", "-n", "--ns":
		return true
	}
	return false
}
