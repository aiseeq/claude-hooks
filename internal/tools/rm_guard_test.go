package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// rmGuardEnv окружение, в котором снимались вердикты Claude Code: HOME и PWD
// заданы, остальные переменные из таблицы — нет
var rmGuardEnv = map[string]string{"HOME": "/home/u", "PWD": "/home/u/proj"}

func newRmGuard(t *testing.T) *RmGuardTool {
	t.Helper()
	tool, err := NewRmGuardTool(core.ToolConfig{Enabled: true}, testLogger(t))
	if err != nil {
		t.Fatalf("NewRmGuardTool: %v", err)
	}
	tool.lookupEnv = func(name string) (string, bool) {
		v, ok := rmGuardEnv[name]
		return v, ok
	}
	return tool
}

func validateRm(t *testing.T, command string) *core.ValidationResult {
	t.Helper()
	result, err := newRmGuard(t).ValidateTool(context.Background(),
		&core.ToolInput{ToolName: "Bash", Command: command, CWD: "/home/u/proj"})
	if err != nil {
		t.Fatalf("ValidateTool: %v", err)
	}
	return result
}

// TestRmGuardMatchesClaudeCode сверяет хук с вердиктами самого Claude Code
// 2.1.305: каждую команду прогнали через claude -p в bypassPermissions, true —
// Claude Code остановил её запросом «Dangerous rm operation on possibly-empty
// variable path». Исключение — false && rm -f $HOME/*: там запрос о критическом
// пути /home/…/*, переписывание с ${HOME:?} от него не спасает, хук его не трогает
func TestRmGuardMatchesClaudeCode(t *testing.T) {
	for _, tc := range []struct {
		command string
		deny    bool
	}{
		{`S=/home/u/sb; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; rm -f "$S"/*.zz`, false},
		{`S=/home/u/sb; rm -f "${S:?}"/*.zz`, false},
		{`rm -f /home/u/sb/*.zz`, false},
		{`S=/home/u/sb; rm -f "$S/"`, false},
		{`S=/home/u/sb; rm -f "$S/file1.zz"`, false},
		{`S=/home/u/sb; rm -f $S/.hidden`, false},
		{`S=/home/u/sb; ls $S && rm -f ${S}/*.zz`, true},
		{`S=/home/u/sb; echo x | rm -f $S/*.zz`, false},
		{`S=/home/u/sb; X=$(rm -f $S/*.zz)`, false},
		{`S=/home/u/sb; echo "$(rm -f $S/*.zz)"`, true},
		{"S=/home/u/sb; cat <<EOT\nrm -f $S/*.zz\nEOT", false},
		{"S=/home/u/sb; cat <<'EOT'\nrm -f $S/*.zz\nEOT", false},
		{`S=/home/u/sb; echo "rm -f $S/*.zz"`, false},
		{`S=/home/u/sb; echo 'a; rm -f $S/*.zz'`, false},
		{`S=/home/u/sb; echo "a && rm -f $S/*.zz"`, false},
		{`S=/home/u/sb; bash -c "rm -f $S/*.zz"`, false},
		{`bash -c 'rm -f "$1"/*.zz' _ /home/u/sb`, false},
		{`false && rm -f "$1"/*.zz`, true},
		{`false && rm -f $UNSETVAR_XYZ/tmp`, true},
		{`D=/home/u/sb; false && rm -f $D/tmp`, false},
		{`false && rm -f $HOME/*`, false},
		{`D=$(pwd); false && rm -rf "$D"`, true},
		{`S=/home/u/sb; find /home/u/sb -name '*.qq' -exec rm -f $S/{} \;`, false},
		{`S=/home/u/sb; ls /home/u/sb | xargs -I{} rm -f $S/{}.zz`, false},
		{`S=/home/u/sb; eval "rm -f $S/*.zz"`, false},
		{`S=/home/u/sb; trap 'rm -f "$S"/*.zz' EXIT; echo t`, false},
		{`S=/home/u/sb; rm -f '$S'/*.zz`, false},
		{`S=/home/u/sb; (rm -f $S/*.zz)`, true},
		{`S=/home/u/sb; { rm -f $S/*.zz; }`, true},
		{`S=/home/u/sb; rm -f ${S:-}/*.zz`, true},
		{`S=/home/u/sb; rm -f -- $S/*.zz`, false},
		{`S=/home/u/sb/nodir; rmdir $S/`, false},
		{`S=/home/u/sb; echo rm -f $S/*.zz`, false},
		{`S=/home/u/sb; true # rm -f $S/*.zz`, false},
		{`F=$(dirname /home/u/sb/x); false && rm -rf "$F"`, false},
		{`T=$(mktemp -d); rm -rf "$T"`, false},
		{`S=/home/u/sb; rm -f $S/$NOPE_N`, true},
		{`false && rm -f $1/x`, false},
		{`S=/home/u/sb; command rm -f $S/*.zz`, false},
		{`S=/home/u/sb; \rm -f $S/*.zz`, false},
		{`S=/home/u/sb; /bin/rm -f $S/*.zz`, false},
		{`S=/home/u/sb; timeout 5 rm -f $S/*.zz`, false},
		{"S=/home/u/sb\nrm -f $S/*.zz", false},
		{`false && rm -f "$PREFIX_UNSET_Q/usr"`, true},
		{`false && rm -f "${PREFIX_UNSET_Q:?}/usr"`, false},
		{`S=/home/u/sb; rm -f $S/b.zz`, false},
		{`S=/home/u/sb; git log -1 --format=%s -m "x; rm -f $S/*" >/dev/null 2>&1; true`, false},
		{`S=/home/u/sb; sh -c 'rm -f "$S"/*.zz'`, true},
		{`S=/home/u/sb; sudo -n rm -f $S/*.zz`, false},
		{"S=/home/u/sb; cat > /home/u/sb/x.sh <<'EOT'\nS=foo\nrm -rf $S/*\nEOT", false},
		{`S=/home/u/sb; rm -f "$S"/a.zz "$S"/b.zz`, false},
		{`S=/home/u/sb; rm -f ${S}/*.zz`, true},
		{`S=/home/u/sb; ls $S && rm -f $S/*.zz`, false},
		{`S=/home/u/sb; for f in a b; do echo $f; done; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; cd /home/u/sb; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; echo "$(date)"; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; X=$(date); rm -f $S/*.zz`, false},
		{`S=/home/u/sb; (echo hi); rm -f $S/*.zz`, true},
		{`S=/home/u/sb; if true; then rm -f $S/*.zz; fi`, false},
		{`S=/home/u/sb; while false; do :; done; rm -f $S/*.zz`, false},
		{`S="/home/u/sb"; rm -f $S/*.zz`, false},
		{`S=$PWD/x; false && rm -f $S/*.zz`, true},
		{`S=$(mktemp -d); false && rm -rf $S/*`, true},
		{`export S=/home/u/sb; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; echo a > /dev/null; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; rm -f $S/*.zz 2>/dev/null`, false},
		{`S=/home/u/sb; rm -f $S/*.zz || true`, false},
		{`S=/home/u/sb; cat /home/u/sb/a.tmp | head -1; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; [ -d $S ] && rm -f $S/*.zz`, false},
		{`S=/home/u/sb; test -d "$S" && rm -f "$S"/*.zz`, false},
		{`S=/home/u/sb; mkdir -p $S; rm -f $S/*.zz; ls $S`, false},
		{`S=/home/u/sb; echo $S; rm -f $S/*.zz; echo done`, false},
		{`ls /home/u/sb; S=/home/u/sb; rm -f $S/*.zz; ls /home/u/sb`, true},
		{`S=/home/u/sb; S2=$S/sub; rm -f $S2/*.zz`, false},
		{`for S in /home/u/sb; do rm -f $S/*.zz; done`, true},
		{`S=/home/u/sb; sleep 0 && rm -f "$S"/*.zz && echo ok`, false},
		{`S=/home/u/sb; rm -rf $S/*.zz`, false},
		{`S=/home/u/sb/nodir; rm -rf $S/`, false},
		{`S=/home/u/sb; rm -f $S/*`, false},
		{`S=/home/u/sb; ls $S | wc -l; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; echo "${S}"; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; echo $((1+1)); rm -f $S/*.zz`, false},
		{`S=/home/u/sb; echo "a" | grep -q a && rm -f $S/*.zz`, false},
		{"S=/home/u/sb; cat <<'EOT' >/dev/null\nhello\nEOT\nrm -f $S/*.zz", true},
		{`S=/home/u/sb; rm -f $S/*.zz # cleanup`, false},
		{`S=/home/u/sb && rm -f $S/*.zz`, false},
		{`S=/home/u/sb; rm -f "$S"/*.zz "$S"/*.yy`, false},
		{`S=/home/u/sb; ls $(echo /home/u/sb) >/dev/null; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; f() { :; }; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; rm -f $S/*.zz; for i in 1; do :; done`, false},
		{"S=/home/u/sb; echo `date`; rm -f $S/*.zz", true},
		{`S=/home/u/sb; rm -f $S/*.zz; X=1`, false},
		{`S=/home/u/sb; rm -f $S/*.zz; echo ${S}`, true},
		{`S=/home/u/sb; rm -f $S/*.zz; echo "$(date)"`, true},
		{`S=/home/u/sb; echo hi; X=1; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; for f in a; do :; done; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; echo '${x}'; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; echo '$(date)'; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; cat <<<hi; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; { echo; }; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; rm -f $S/*.zz; (echo)`, true},
		{"S=/home/u/sb; rm -f $S/*.zz; cat <<'EOT' >/dev/null\nx\nEOT", true},
		{`cd /home/u/sb && S=/home/u/sb; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; echo a | read X; rm -f $S/*.zz`, false},
		{`S=/home/u/sb T=/home/u/sb; rm -f $S/*.zz`, true},
		{`S=/home/u/sb false && rm -f $S/*.zz`, true},
		{`declare S=/home/u/sb; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; X=$(date) Y=1; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; if [ -n "$S" ]; then rm -rf "$S"/*.zz; fi`, false},
		{`S=/home/u/sb; rm -f $S/*.zz && echo "done: $(date)"`, false},
		{"S=/home/u/sb; X=`date`; rm -f $S/*.zz", false},
		{`S=/home/u/sb; S=$S/sub; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; unset S; false && rm -f $S/*.zz`, true},
		{`S=/home/u/sb; echo x; rm -f $S/*.zz; S=/home/u/sb; rm -f $S/*.yy`, true},
		{`export S=/home/u/sb T=/home/u/sb; rm -f $T/*.zz`, false},
		{`S=/home/u/sb; rm -f "$S"/*.zz $(echo /home/u/sb/none.zz)`, true},
		{"S=/home/u/sb\necho a\nrm -f $S/*.zz", false},
		{`S=/home/u/sb; echo a; echo b; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; ls /home/u/sb | while read f; do :; done; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; [[ -d $S ]] && rm -f $S/*.zz`, false},
		{`S=/home/u/sb; case x in x) rm -f $S/*.zz;; esac`, false},
		{`S=/home/u/sb; time rm -f $S/*.zz`, false},
		{`S=/home/u/sb; nohup rm -f $S/*.zz >/dev/null 2>&1`, false},
		{`S=/home/u/sb; rm -f $S/*.zz &`, false},
		{`S="/home/u/sb/x y"; rm -f "$S"/*.zz`, false},
		{`S=~/nonexist-q; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; S2="$S"; rm -f $S2/*.zz`, false},
		{`S=/home/u/sb; X=1 echo hi; rm -f $S/*.zz`, false},
		{`true; S=/home/u/sb; rm -f $S/*.zz`, true},
		{`echo $((1)); S=/home/u/sb; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; rm -f $S/*.zz; for f in a; do echo $f; done`, true},
		// второй прогон: команды, которых не было при выводе правила
		{`T=$(mktemp -d); false && rm -rf "$T"`, false},
		{`T=$(mktemp -d); false && rm -rf "$T"/*`, true},
		{`D=/home/u/sb; for f in $D/*.zz; do rm -f "$f"; done`, false},
		{`D=/home/u/sb; rm -f "$D"/a.zz "$D"/b.zz && ls $D`, false},
		{`D=/home/u/sb; cd $D && rm -f ./*.zz`, false},
		{`OUT=/home/u/sb; rm -rf "$OUT"/*.zz; mkdir -p "$OUT"`, false},
		{`cd /home/u/sb && OUT=/home/u/sb && rm -rf "$OUT"/*.zz`, true},
		{`S=/home/u/sb; rm -f $S/*.zz; echo "removed from ${S}"`, true},
		{`S=/home/u/sb; rm -f "$S"/*.zz 2>&1 | tail -1`, false},
		{`ls /home/u/sb >/dev/null && S=/home/u/sb && rm -f $S/*.zz`, true},
		{`false && rm -rf "$WORKDIR_UNSET_Q"/*`, true},
		{`false && rm -rf "${WORKDIR_UNSET_Q:?}"/*`, false},
		{`S=/home/u/sb; X=$(ls $S | wc -l); rm -f $S/*.zz`, false},
		{`S=/home/u/sb; echo "$(ls $S)" >/dev/null; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; rm -f $S/*.zz $S/*.yy`, false},
		{`S=/home/u/sb; printf '%s\n' a; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; rm -f "$S"/*.zz; echo 'done ${x}'`, false},
		{`S=/home/u/sb; rm -fv -- "$S"/*.zz`, false},
		{`S=/home/u/sb; rm -f $S/*.zz # ${not}`, false},
		{`S=/home/u/sb; awk '{print $1}' /home/u/sb/a.tmp; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; echo $S/*.zz | xargs rm -f`, false},
		{`S=/home/u/sb; git -C /home/u/sb status 2>/dev/null || rm -f $S/*.zz`, false},
		{`S=/home/u/sb; S="$S"; rm -f $S/*.zz`, false},
		{`S=/home/u/sb; rm -f $S/*.zz; T=/home/u/sb; rm -f $T/*.zz`, true},
		{`S=/home/u/sb; (cd $S && ls) >/dev/null; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; rm -f "$S"/{a,b}.zz`, true},
		{`S=/home/u/sb; rm -f $S/[ab].zz`, false},
		{`false && rm -rf "$1/"`, true},
		{`f() { rm -rf "$1"/*.zz; }; f /home/u/sb`, false},
		{`S=/home/u/sb; diff <(ls $S) <(ls /home/u/sb); rm -f $S/*.zz`, true},
		{`S=/home/u/sb; echo $(( $(ls $S | wc -l) + 1 )); rm -f $S/*.zz`, true},
		{`S=/home/u/sb; rm -f $S/*.zz && echo ok || echo fail`, false},
		{`S=/home/u/sb; rmdir $S/nodir 2>/dev/null; true`, false},
		{`export S=/home/u/sb; bash -c 'rm -f "$S"/*.zz'`, false},
		{`S=/home/u/sb; sh -c "rm -f '$S'/*.zz"`, false},
		{`S=/home/u/sb; timeout 10 bash -c "rm -f $S/*.zz"`, false},
		{`S=/home/u/sb; echo ${#S}; rm -f $S/*.zz`, true},
		{`S=/home/u/sb; ls -la $S | head -3; rm -f "$S"/*.zz; ls $S`, false},
		{`S=/home/u/sb; mkdir -p "$S" && touch "$S/x.zz" && rm -f "$S"/*.zz`, false},
		{`false && sudo rm -rf $NOT_SET_Q/*`, true},
	} {
		r := validateRm(t, tc.command)
		if r.IsValid == tc.deny {
			t.Errorf("deny=%v, ожидалось %v: %q %v", !r.IsValid, tc.deny, tc.command, r.Suggestions)
		}
	}
}

// TestRmGuardExtra формы, которых не было в прогоне Claude Code: отклонение
// выведено из того же правила
func TestRmGuardExtra(t *testing.T) {
	for _, tc := range []struct {
		command string
		deny    bool
	}{
		// случай из сессии лида: перед присваиванием есть другие команды
		{`cd /home/u/proj && ls; S=/home/u/scratchpad/sl; rm -f $S/*; ls`, true},
		{"T=$(mktemp -d)\nfalse && rm -rf \"$T\"/*", true},
		{`T=$(mktemp -d); rm -rf "${T:?}"/*`, false},
		{`false && rm -rf "$NOPE"/*.log | tee /dev/null`, true},
		{`false && xargs rm -f $NOPE/*`, true},
		{`false && find . -name x -exec rm -rf $NOPE/{} +`, true},
		{`echo $(false && rm -rf $NOPE/*)`, true},
		{"cat <<EOF\nrm -rf $NOPE/*\nEOF", false},
		{`grep -n 'rm -rf $NOPE/*' notes.txt`, false},
		{`echo "rm -rf $NOPE/*" > notes.txt`, false},
		{`git commit -m "rm -rf $NOPE/* больше не нужен"`, false},
		{`false && rm -rf /home/u/sb/*`, false},
		{`false && rm -rf "$NOPE"`, false},
		{`false && rm -rf $NOPE/build`, false},
		{`D=$(git rev-parse --show-toplevel); false && rm -rf "$D"`, true},
		{`D=$(cd /home/u/proj/x && pwd); false && rm -rf "$D"`, false},
	} {
		r := validateRm(t, tc.command)
		if r.IsValid == tc.deny {
			t.Errorf("deny=%v, ожидалось %v: %q %v", !r.IsValid, tc.deny, tc.command, r.Suggestions)
		}
	}
}

// TestRmGuardReason причина доходит до агента с готовой переписанной формой
func TestRmGuardReason(t *testing.T) {
	for _, tc := range []struct {
		command, want string
	}{
		{`cd /x && ls; S=/home/u/scratchpad/sl; rm -f $S/*; ls`,
			"`rm -f $S/*`: перепиши `rm -f \"${S:?}\"/*` или укажи литеральный путь"},
		{`false && rm -rf "$NOPE/"`,
			"`rm -rf \"$NOPE/\"`: перепиши `rm -rf \"${NOPE:?}/\"` или укажи литеральный путь"},
		{`false && rm -f ${NOPE:-}/*.zz`,
			"`rm -f ${NOPE:-}/*.zz`: перепиши `rm -f \"${NOPE:?}\"/*.zz` или укажи литеральный путь"},
		{`false && rm -f "$1"/*.zz`,
			"`rm -f \"$1\"/*.zz`: $1 в команде Bash пуст, укажи литеральный путь"},
		{`D=$(pwd); false && rm -rf "$D"`,
			"`rm -rf \"$D\"`: $D задан из `$(pwd)` и может оказаться рабочей папкой, защита ${D:?} не поможет, укажи литеральный путь"},
		{`S=/x; sh -c 'rm -f "$S"/*.zz'`,
			"`rm -f \"$S\"/*.zz`: внутри sh -c '…' переменная $S пуста, укажи литеральный путь или передай его аргументом: sh -c 'rm -f \"$1\"/*' _ \"$S\""},
	} {
		r := validateRm(t, tc.command)
		if r.IsValid {
			t.Errorf("должно отклоняться: %q", tc.command)
			continue
		}
		if len(r.Violations) == 0 || !strings.Contains(r.Violations[0].Message, "Claude Code спросит пользователя") {
			t.Errorf("нет заголовка причины: %q %v", tc.command, r.Violations)
		}
		if !strings.Contains(strings.Join(r.Suggestions, "\n"), tc.want) {
			t.Errorf("%q:\nесть  %q\nждали %q", tc.command, r.Suggestions, tc.want)
		}
	}
}
