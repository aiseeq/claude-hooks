package tools

import (
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// RmGuardTool отклоняет rm и rmdir, на которые Claude Code сам остановит
// команду запросом «Dangerous rm operation on possibly-empty variable path»:
// этот запрос не снимается ни правилами, ни режимом bypassPermissions и ждёт
// пользователя. Хук отвечает раньше и даёт агенту переписанную форму.
//
// Правило повторяет Claude Code 2.1.305 (проверено прогоном команд через
// claude -p, таблица в тесте). Цель — переменная со слешем и глобом, слешем,
// ещё одной переменной или концом слова ($S/*, "$S/", $S/$N); позиционный
// параметр так же ("$1"/*); переменная перед каталогом верхнего уровня
// ($X/tmp), если её нет ни в команде, ни в окружении; переменная из $(pwd),
// $(git rev-parse …), $(dirname …), $(cd …). Переменную, которой команда
// в начале присвоила обычное значение, Claude Code подставляет и не спрашивает,
// но только пока команда простая: без ${…}, подоболочек, групп { }, функций,
// here-doc, аргумента из одной подстановки $(…) и без переприсваивания
// упомянутых переменных после первой команды.
type RmGuardTool struct {
	*BaseTool
	lookupEnv func(string) (string, bool)
}

// NewRmGuardTool создает проверку rm по переменной
func NewRmGuardTool(config core.ToolConfig, logger core.Logger) (*RmGuardTool, error) {
	return &RmGuardTool{
		BaseTool:  NewBaseTool("rm_guard", config.Enabled, []string{"Bash"}, logger),
		lookupEnv: os.LookupEnv,
	}, nil
}

const rmGuardHeader = "rm по переменной без защиты: Claude Code спросит пользователя и будет ждать ответа. Перепиши команду:"

const (
	reName  = `[A-Za-z_][A-Za-z0-9_]*`
	reDflt  = `(?::?-(?:["']{2}|"?\$\{?` + reName + `\}?"?)?)?`
	reAfter = `[\x{E020}"']*\\?/(?:[*?\[{]|\$|/|["']|\x{E020}|$)`
	reTop   = `bin|boot|dev|etc|home|lib|lib32|lib64|libx32|media|mnt|opt|proc|root|run|sbin|srv|sys|tmp|usr|var|snap|nix|lost\+found|private|cores|Applications|Library|System|Users|Volumes|Windows|ProgramData|cygdrive`
)

var (
	// namedVarTarget $VAR/ перед глобом, слешем, переменной или концом слова
	namedVarTarget = regexp.MustCompile(`^[\x{E020}"']*\$(?:\{` + reName + reDflt + `\}|` + reName + `)` + reAfter)
	// positionalTarget то же для $1, $@, $*, $!
	positionalTarget = regexp.MustCompile(`^[\x{E020}"']*\$(?:\{(?:[0-9]+|[@*!])` + reDflt + `\}|[0-9@*!])` + reAfter)
	// topDirTarget $VAR/<каталог верхнего уровня>
	topDirTarget = regexp.MustCompile(`(?i)^[\x{E020}"']*\$(?:\{(` + reName + `)(?::?-(?:["']{2}|"?\$\{?(` + reName + `)\}?"?)?)?\}|(` + reName + `))[\x{E020}"']*\\?/(` + reTop + `)(?:[\\/]+\*+)*/?[\x{E020}"']*$`)
	// guardable раскрытие переменной, которое переписывается в ${VAR:?}
	guardable = regexp.MustCompile(`\$(?:\{(` + reName + `|[0-9]+|[@*!])(?::?-(?:["']{2}|"?\$\{?(` + reName + `)\}?"?)?)?\}|(` + reName + `|[0-9]+|[@*!]))`)
	// bareVar слово целиком — одна переменная: "$D", ${D}, $D/
	bareVar        = regexp.MustCompile(`^[\x{E020}"']*\$(?:\{(` + reName + `)(?::?[?-][^}]*)?\}|(` + reName + `))[\x{E020}"']*$`)
	assignWord     = regexp.MustCompile(`^(` + reName + `)\+?=`)
	plainName      = regexp.MustCompile(`^` + reName + `$`)
	braceExpansion = regexp.MustCompile(`\{[^{}'"]*(?:,|\.\.)[^{}'"]*\}`)
	lastPathPart   = regexp.MustCompile(`/+[^/]*/*$`)
	singleQuoted   = regexp.MustCompile(`^'[^']*\$`)
	positionalNm   = regexp.MustCompile(`\$\{?([0-9]+|[@*!])`)
	dotSlashes     = regexp.MustCompile(`/(?:\./)+`)
	manySlashes    = regexp.MustCompile(`/{2,}`)
	dirPrinting    = regexp.MustCompile(`^\s*(?:(cd|pwd|dirname)(?:[^A-Za-z0-9_-]|$)|git\s+(rev-parse)\b)`)
	dirnameOrCd    = regexp.MustCompile(`^\s*(?:cd|dirname)\s+(?:--?\w*\s+)*([^\s;&|]*)`)
	normallySet    = map[string]bool{"HOME": true, "USERPROFILE": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true, "XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true, "PWD": true, "OLDPWD": true, "TMPDIR": true, "TMP": true, "TEMP": true}
	rmKeywords     = map[string]bool{"do": true, "then": true, "else": true, "elif": true, "if": true, "while": true, "until": true, "!": true, "coproc": true, "done": true, "fi": true, "esac": true}
	rmWrappers     = map[string]bool{"sudo": true, "doas": true, "exec": true, "command": true, "env": true, "nice": true, "nohup": true, "busybox": true, "time": true, "timeout": true, "stdbuf": true, "setsid": true, "ionice": true, "pkexec": true, "runuser": true, "xargs": true}
	inlineShells   = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "ash": true, "mksh": true}
	declarers      = map[string]bool{"export": true, "declare": true, "typeset": true, "readonly": true, "local": true}
)

// ValidateTool проверяет цели rm и rmdir в команде Bash
func (t *RmGuardTool) ValidateTool(_ context.Context, input *core.ToolInput) (*core.ValidationResult, error) {
	cmd := input.Command
	if !t.IsEnabled() || input.ToolName != "Bash" || !strings.Contains(cmd, "rm") || !strings.Contains(cmd, "$") {
		return &core.ValidationResult{IsValid: true}, nil
	}
	script := &shScript{}
	parseShell(cmd, false, script)
	vars := t.trackVars(script, input.CWD)
	var reasons []string
	for _, c := range script.cmds {
		reasons = append(reasons, t.checkCommand(c.words, vars, inlineNone)...)
	}
	if len(reasons) == 0 {
		return &core.ValidationResult{IsValid: true}, nil
	}
	reasons = deduplicateStrings(reasons)
	t.Logger().Debug("rm by possibly-empty variable denied", "reasons", len(reasons))
	violation := core.NewViolation("rm_guard", rmGuardHeader, reasons[0], core.LevelCritical, 0, 0)
	return core.NewValidationResult(false, []core.Violation{violation}, reasons), nil
}

// rmVars что Claude Code знает о переменных команды
type rmVars struct {
	// tracked — Claude Code подставляет значения переменных; иначе любая
	// переменная в цели считается возможно пустой
	tracked bool
	// resolved переменные с известным значением
	resolved map[string]bool
	// assigned переменные, которым команда что-то присваивает
	assigned map[string]bool
	// derived переменные из вывода pwd и подобных: имя → источник
	derived map[string]string
	// exported переменные, переданные export или declare -x дочерним процессам
	exported map[string]bool
	// positionalBound — $1… заданы (функция, set --, аргументы sh -c)
	positionalBound bool
	lookupEnv       func(string) (string, bool)
}

// binding одно присваивание переменной
type binding struct {
	name, value string
	leading     bool // в цепочке присваиваний до первой обычной команды
	bare        bool // простое присваивание, не prefix перед командой и не цикл
}

// trackVars разбирает присваивания так же, как Claude Code решает, можно ли
// подставить значения переменных
func (t *RmGuardTool) trackVars(s *shScript, cwd string) *rmVars {
	v := &rmVars{
		tracked:  !s.dollarBrace && !s.group && !s.function && !s.heredoc && !s.complexArith,
		resolved: map[string]bool{}, assigned: map[string]bool{}, derived: map[string]string{},
		exported:        map[string]bool{},
		positionalBound: s.function,
		lookupEnv:       t.lookupEnv,
	}
	refs := v.scanWords(s)
	bindings := v.collectBindings(s)
	v.applyBindings(bindings, refs, cwd)
	return v
}

// scanWords имена всех упомянутых переменных; аргумент из одной подстановки
// $(…) останавливает отслеживание
func (v *rmVars) scanWords(s *shScript) map[string]bool {
	refs := map[string]bool{}
	for _, c := range s.cmds {
		for _, w := range c.words {
			for _, name := range varRefs(w.text) {
				refs[name] = true
			}
			if !w.redirect && (w.raw == string(substMark) || w.raw == `"`+string(substMark)+`"`) {
				v.tracked = false
			}
		}
	}
	return refs
}

// collectBindings присваивания команд верхнего уровня по порядку
func (v *rmVars) collectBindings(s *shScript) []binding {
	var bindings []binding
	leading := true
	for _, c := range s.cmds {
		if c.nested {
			continue
		}
		var b []binding
		b, leading = commandBindings(c.words, leading, v)
		for _, x := range b {
			// S="$S" значения не меняет, и Claude Code его не замечает
			if m := bareVar.FindStringSubmatch(x.value); m != nil && m[1]+m[2] == x.name {
				continue
			}
			bindings = append(bindings, x)
		}
	}
	return bindings
}

// applyBindings отслеживание ломает упомянутая переменная, присвоенная не в
// начале, не простым присваиванием или больше одного раза
func (v *rmVars) applyBindings(bindings []binding, refs map[string]bool, cwd string) {
	count := map[string]int{}
	for _, b := range bindings {
		count[b.name]++
		v.assigned[b.name] = true
		if refs[b.name] && (!b.leading || !b.bare) {
			v.tracked = false
		}
	}
	for name, n := range count {
		if n > 1 && refs[name] {
			v.tracked = false
		}
	}
	for _, b := range bindings {
		if b.leading && b.bare && count[b.name] == 1 && v.valueResolved(b.value) {
			v.resolved[b.name] = true
		}
		v.trackDerived(b, cwd)
	}
}

// commandBindings присваивания одной простой команды; второй результат —
// остаётся ли цепочка ведущих присваиваний
func commandBindings(words []shWord, leading bool, v *rmVars) ([]binding, bool) {
	words = stripKeywords(words)
	var out []binding
	i := 0
	for i < len(words) && assignWord.MatchString(words[i].raw) {
		out = append(out, assignment(words[i], leading, true))
		i++
	}
	if i == len(words) {
		if len(out) > 1 {
			v.tracked = false // S=a T=b одной командой
		}
		return out, leading
	}
	if i > 0 {
		for k := range out {
			out[k].bare = false // S=a cmd: S задана только для cmd
		}
	}
	name := commandName(words[i].raw)
	args := words[i+1:]
	if declarers[name] {
		return append(out, declBindings(name, args, leading, v)...), leading
	}
	return append(out, commandSideBindings(name, args, v)...), false
}

// declBindings присваивания export, declare, local и подобных
func declBindings(name string, args []shWord, leading bool, v *rmVars) []binding {
	var out []binding
	exporting := name == "export"
	for _, w := range args {
		switch {
		case strings.HasPrefix(w.raw, "-") && strings.Contains(w.raw, "x"):
			exporting = true
		case assignWord.MatchString(w.raw):
			b := assignment(w, leading, true)
			if exporting {
				v.exported[b.name] = true
			}
			out = append(out, b)
		case exporting && assignName(w.raw):
			v.exported[w.raw] = true
		}
	}
	return out
}

// commandSideBindings переменные, которые задаёт или снимает сама команда:
// for, select, read, unset; set -- задаёт позиционные параметры
func commandSideBindings(name string, args []shWord, v *rmVars) []binding {
	var out []binding
	switch name {
	case "for", "select":
		if len(args) > 0 {
			out = append(out, binding{name: args[0].raw})
		}
	case "read", "unset":
		for _, w := range args {
			if !strings.HasPrefix(w.raw, "-") && assignName(w.raw) {
				out = append(out, binding{name: w.raw})
			}
		}
	case "set":
		if len(args) > 1 && args[0].raw == "--" {
			v.positionalBound = true
		}
	}
	return out
}

func assignment(w shWord, leading, bare bool) binding {
	m := assignWord.FindStringSubmatch(w.raw)
	value := w.text[strings.IndexByte(w.text, '=')+1:]
	return binding{name: m[1], value: value, leading: leading, bare: bare}
}

func assignName(s string) bool {
	return plainName.MatchString(s)
}

// valueResolved значение известно до запуска: без подстановок, ~ и
// переменных с неизвестным значением
func (v *rmVars) valueResolved(value string) bool {
	if strings.Contains(value, "$(") || strings.Contains(value, "`") || strings.HasPrefix(unquoteWord(value), "~") {
		return false
	}
	for _, name := range varRefs(value) {
		if !v.resolved[name] {
			return false
		}
	}
	return true
}

// trackDerived помечает переменную, заданную выводом pwd, git rev-parse,
// dirname или cd: защита ${D:?} не помогает, значение не пустое
func (v *rmVars) trackDerived(b binding, cwd string) {
	value := strings.TrimSuffix(strings.TrimPrefix(b.value, `"`), `"`)
	value = strings.TrimRight(value, "/")
	if name := bareVar.FindStringSubmatch(value); name != nil {
		n := name[1] + name[2]
		switch {
		case n == "PWD":
			v.derived[b.name] = "$PWD"
		case v.derived[n] != "":
			v.derived[b.name] = v.derived[n]
		default:
			delete(v.derived, b.name)
		}
		return
	}
	inner, ok := substBody(value)
	if !ok || !printsDir(inner, cwd) {
		delete(v.derived, b.name)
		return
	}
	v.derived[b.name] = value
}

// substBody тело $(…) или `…`, если значение целиком из подстановки
func substBody(value string) (string, bool) {
	if strings.HasPrefix(value, "`") && strings.HasSuffix(value, "`") && len(value) > 1 {
		return value[1 : len(value)-1], true
	}
	if !strings.HasPrefix(value, "$(") {
		return "", false
	}
	src := []rune(value)
	end := matchParen(src, 1)
	if end != len(src)-1 {
		return "", false
	}
	return string(src[2:end]), true
}

// printsDir подстановка печатает каталог, который может оказаться рабочей
// папкой, корнем репозитория или системной папкой
func printsDir(inner, cwd string) bool {
	m := dirPrinting.FindStringSubmatch(inner)
	if m == nil {
		return false
	}
	if m[2] == "rev-parse" || m[1] == "pwd" {
		return true
	}
	arg := dirnameOrCd.FindStringSubmatch(inner)
	if arg == nil || arg[1] == "" || strings.ContainsAny(arg[1], "$`") || arg[1] == `""` || arg[1] == "''" {
		return true
	}
	if m[1] == "dirname" {
		return criticalDir(parentDir(unquoteWord(arg[1])), cwd)
	}
	parts := strings.Split(inner[len(arg[0]):], "&&")
	if strings.TrimSpace(parts[0]) != "" {
		return true
	}
	for _, p := range parts[1:] {
		if strings.ContainsAny(p, ";|&") {
			return true
		}
	}
	return false
}

// parentDir то, что напечатает dirname
func parentDir(p string) string {
	trimmed := lastPathPart.ReplaceAllString(p, "")
	if trimmed != "" {
		return trimmed
	}
	if strings.HasPrefix(p, "/") {
		return "/"
	}
	return "."
}

// criticalDir корень, каталог верхнего уровня, домашний, рабочий или его предок
func criticalDir(p, cwd string) bool {
	if !path.IsAbs(p) {
		p = path.Join(cwd, p)
	}
	p = path.Clean(p)
	home, err := os.UserHomeDir()
	if err == nil && p == path.Clean(home) {
		return true
	}
	return p == "/" || path.Dir(p) == "/" || p == path.Clean(cwd) || strings.HasPrefix(path.Clean(cwd)+"/", p+"/")
}

// inline откуда взят текст команды
type inline int

const (
	inlineNone     inline = iota
	inlineSingle          // sh -c '…': переменные внешней оболочки не видны
	inlineReparsed        // eval или sh -c "…": текст разбирается повторно
)

// checkCommand возвращает причины отказа для одной простой команды
func (t *RmGuardTool) checkCommand(words []shWord, v *rmVars, src inline) []string {
	words = stripKeywords(words)
	i := 0
	for i < len(words) && (assignWord.MatchString(words[i].raw) || words[i].redirect ||
		strings.HasPrefix(words[i].raw, "$") || strings.HasPrefix(words[i].raw, string(substMark))) {
		i++
	}
	i = skipWrappers(words, i)
	if i >= len(words) {
		return nil
	}
	name := commandName(words[i].raw)
	args := words[i+1:]
	switch {
	case name == "rm" || name == "rmdir":
		return t.checkTargets(words[i:], v, src)
	case name == "eval":
		return t.checkInline(joinText(args), v, inlineReparsed)
	case name == "trap":
		if len(args) > 0 && args[0].raw == "--" {
			args = args[1:]
		}
		if len(args) > 0 {
			return t.checkInline(unquoteWord(args[0].text), v, src)
		}
	case inlineShells[name]:
		return t.checkShellC(args, v)
	case name == "find":
		return t.checkFindExec(args, v, src)
	}
	return nil
}

// skipWrappers пропускает sudo, timeout, xargs и подобные с их флагами
func skipWrappers(words []shWord, i int) int {
	for i < len(words) && rmWrappers[commandName(words[i].raw)] {
		i++
		for n := wrapperArgLen(words, i); n > 0; n = wrapperArgLen(words, i) {
			i += n
		}
	}
	return i
}

// wrapperArgLen сколько слов с позиции i — флаги и значения обёртки
// (sudo -u root, timeout 5), 0 — дальше сама команда
func wrapperArgLen(words []shWord, i int) int {
	if i >= len(words) || startsCommand(words[i].raw) {
		return 0
	}
	w := words[i].raw
	switch {
	case words[i].redirect:
		return 1
	case strings.HasPrefix(w, "-"):
		if i+1 < len(words) {
			next := words[i+1].raw
			if !strings.HasPrefix(next, "-") && !strings.Contains(next, "=") && !startsCommand(next) {
				return 2 // флаг со значением
			}
		}
		return 1
	case assignWord.MatchString(w) || (w != "" && w[0] >= '0' && w[0] <= '9') || strings.HasPrefix(w, "$"):
		return 1
	}
	return 0
}

// startsCommand слово — rm, rmdir или ещё одна обёртка
func startsCommand(raw string) bool {
	name := commandName(raw)
	return name == "rm" || name == "rmdir" || rmWrappers[name]
}

// checkInline разбирает строку eval, trap или sh -c как отдельную команду
func (t *RmGuardTool) checkInline(text string, v *rmVars, src inline) []string {
	inner := &shScript{}
	parseShell(text, false, inner)
	var reasons []string
	for _, c := range inner.cmds {
		reasons = append(reasons, t.checkCommand(c.words, v, src)...)
	}
	return reasons
}

// checkShellC проверяет скрипт sh -c: в одинарных кавычках переменные
// внешней оболочки ему не видны, в двойных их раскрывает внешняя оболочка
func (t *RmGuardTool) checkShellC(args []shWord, v *rmVars) []string {
	for k, w := range args {
		if !strings.HasPrefix(w.raw, "-") || !strings.Contains(w.raw[1:], "c") || k+1 >= len(args) {
			continue
		}
		script := args[k+1]
		if !strings.HasPrefix(script.raw, "'") {
			return t.checkInline(unquoteWord(script.text), v, inlineReparsed)
		}
		// внутренней оболочке видны только экспортированные переменные
		fresh := &rmVars{
			tracked: v.tracked, resolved: map[string]bool{}, assigned: map[string]bool{}, derived: map[string]string{},
			exported: v.exported, positionalBound: k+2 < len(args),
			lookupEnv: v.lookupEnv,
		}
		for name := range v.exported {
			fresh.resolved[name] = v.resolved[name]
			fresh.assigned[name] = v.assigned[name]
		}
		return t.checkInline(unquoteWord(script.text), fresh, inlineSingle)
	}
	return nil
}

// checkFindExec проверяет команду после -exec, -execdir, -ok, -okdir
func (t *RmGuardTool) checkFindExec(args []shWord, v *rmVars, src inline) []string {
	var reasons []string
	start := -1
	for k, w := range args {
		switch r := w.raw; {
		case start < 0 && (r == "-exec" || r == "-execdir" || r == "-ok" || r == "-okdir"):
			start = k + 1
		case start >= 0 && (r == ";" || r == `\;` || r == `';'` || (r == "+" && k > 0 && args[k-1].raw == "{}")):
			reasons = append(reasons, t.checkCommand(args[start:k], v, src)...)
			start = -1
		}
	}
	if start >= 0 {
		reasons = append(reasons, t.checkCommand(args[start:], v, src)...)
	}
	return reasons
}

// checkTargets проверяет аргументы rm или rmdir; words[0] — сама утилита
func (t *RmGuardTool) checkTargets(words []shWord, v *rmVars, src inline) []string {
	var reasons []string
	for k, w := range words[1:] {
		raw := w.raw
		if raw == "" || w.redirect || strings.HasPrefix(raw, "-") || singleQuoted.MatchString(raw) {
			continue
		}
		if reason := t.checkTarget(w, v, src); reason != nil {
			reasons = append(reasons, rmInvocation(words, k+1, "")+": "+reason(words, k+1))
		}
	}
	return reasons
}

// hint собирает подсказку по команде и номеру цели
type hint func(words []shWord, target int) string

// checkTarget возвращает подсказку, если Claude Code спросит про эту цель
func (t *RmGuardTool) checkTarget(w shWord, v *rmVars, src inline) hint {
	raw := w.raw
	if m := bareVar.FindStringSubmatch(trimGlobTail(raw)); m != nil {
		if from := v.derived[m[1]+m[2]]; from != "" {
			name := m[1] + m[2]
			return func([]shWord, int) string {
				return fmt.Sprintf("$%s задан из `%s` и может оказаться рабочей папкой, защита ${%s:?} не поможет, укажи литеральный путь", name, from, name)
			}
		}
	}
	if m := topDirTarget.FindStringSubmatch(normalizeSlashes(raw)); m != nil {
		names := []string{m[1] + m[3]}
		if m[2] != "" {
			names = append(names, m[2])
		}
		if v.allMayBeUnset(names) {
			return guardHint(src, names, "при пустой $"+names[0]+" удалит /"+m[4]+", ")
		}
	}
	if positionalTarget.MatchString(raw) && !v.positionalBound {
		name := positionalNm.FindStringSubmatch(raw)[1]
		return func([]shWord, int) string {
			return fmt.Sprintf("$%s в команде Bash пуст, укажи литеральный путь", name)
		}
	}
	if !namedVarTarget.MatchString(raw) {
		return nil
	}
	names := varRefs(w.text)
	// "$S"/{a,b}: раскрытие скобок Claude Code не подставляет
	if v.tracked && v.allResolved(names) && !braceExpansion.MatchString(raw) {
		return nil
	}
	for _, n := range names {
		if normallySet[n] {
			return func([]shWord, int) string {
				return fmt.Sprintf("$%s обычно задана, защита не поможет, укажи литеральный путь", n)
			}
		}
	}
	return guardHint(src, names, "")
}

// guardHint подсказка с переписанной формой "${VAR:?}"
func guardHint(src inline, names []string, prefix string) hint {
	return func(words []shWord, target int) string {
		switch src {
		case inlineSingle:
			return fmt.Sprintf("%sвнутри sh -c '…' переменная $%s пуста, укажи литеральный путь или передай его аргументом: sh -c 'rm -f \"$1\"/*' _ \"$%s\"", prefix, names[0], names[0])
		case inlineReparsed:
			return prefix + "команда разбирается повторно (eval или sh -c \"…\"), кавычки защиты не сработают, укажи литеральный путь"
		}
		return fmt.Sprintf("%sперепиши %s или укажи литеральный путь", prefix, rmInvocation(words, target, guardTarget(words[target].text)))
	}
}

// allResolved все переменные цели известны (HOME — из окружения)
func (v *rmVars) allResolved(names []string) bool {
	for _, n := range names {
		if v.resolved[n] {
			continue
		}
		if n == "HOME" && !v.assigned[n] {
			if val, ok := v.lookupEnv(n); ok && val != "" {
				continue
			}
		}
		return false
	}
	return true
}

// allMayBeUnset переменные не присвоены командой и пусты в окружении
func (v *rmVars) allMayBeUnset(names []string) bool {
	for _, n := range names {
		if v.assigned[n] || n == "HOME" || n == "PWD" {
			return false
		}
		if val, ok := v.lookupEnv(n); ok && val != "" {
			return false
		}
	}
	return true
}

// guardTarget переписывает каждое раскрытие в ${VAR:?}, вне кавычек — в "${VAR:?}"
func guardTarget(target string) string {
	var b strings.Builder
	last := 0
	for _, m := range guardable.FindAllStringSubmatchIndex(target, -1) {
		b.WriteString(target[last:m[0]])
		name := sub(target, m, 1) + sub(target, m, 3)
		guarded := "${" + name + ":?}"
		if dflt := sub(target, m, 2); dflt != "" {
			guarded = "${" + name + ":-${" + dflt + ":?}}"
		}
		if !insideDoubleQuotes(target[:m[0]]) {
			guarded = `"` + guarded + `"`
		}
		b.WriteString(guarded)
		last = m[1]
	}
	b.WriteString(target[last:])
	return b.String()
}

func sub(s string, m []int, group int) string {
	if m[2*group] < 0 {
		return ""
	}
	return s[m[2*group]:m[2*group+1]]
}

// insideDoubleQuotes после prefix открыта двойная кавычка
func insideDoubleQuotes(prefix string) bool {
	in := false
	for i := 0; i < len(prefix); i++ {
		switch prefix[i] {
		case '\\':
			i++
		case '\'':
			if !in {
				if j := strings.IndexByte(prefix[i+1:], '\''); j >= 0 {
					i += j + 1
				}
			}
		case '"':
			in = !in
		}
	}
	return in
}

// rmInvocation текст вызова rm; replacement заменяет цель target
func rmInvocation(words []shWord, target int, replacement string) string {
	parts := make([]string, 0, len(words))
	for k, w := range words {
		if k == target && replacement != "" {
			parts = append(parts, replacement)
			continue
		}
		parts = append(parts, w.text)
	}
	return "`" + strings.Join(parts, " ") + "`"
}

// stripKeywords снимает do, then, if, ! и подобные в начале команды
func stripKeywords(words []shWord) []shWord {
	for len(words) > 0 && rmKeywords[words[0].raw] {
		words = words[1:]
	}
	return words
}

// commandName имя утилиты: без \ в начале, пути и регистра
func commandName(raw string) string {
	name := strings.TrimPrefix(raw, `\`)
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(name)
}

func joinText(words []shWord) string {
	parts := make([]string, 0, len(words))
	for _, w := range words {
		parts = append(parts, unquoteWord(w.text))
	}
	return strings.Join(parts, " ")
}

// trimGlobTail снимает хвост из / * . перед закрывающими кавычками: "$D/" → "$D"
func trimGlobTail(raw string) string {
	r := []rune(raw)
	end := len(r)
	for end > 0 && (r[end-1] == '"' || r[end-1] == '\'' || r[end-1] == substMark) {
		end--
	}
	start := end
	for start > 0 && strings.ContainsRune("/*.", r[start-1]) {
		start--
	}
	if start < end && r[start] == '/' {
		return string(r[:start]) + string(r[end:])
	}
	return raw
}

// normalizeSlashes /./ и // → /
func normalizeSlashes(s string) string {
	return manySlashes.ReplaceAllString(dotSlashes.ReplaceAllString(s, "/"), "/")
}

func deduplicateStrings(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := items[:0]
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
