package core

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Режим читается из /proc живого процесса, поэтому и проверяется на живом:
// подставные данные тут ничего не доказали бы
func startWithArgs(t *testing.T, args ...string) string {
	t.Helper()
	// Лишние аргументы уходят в позиционные параметры sh, процесс не роняют
	// и ложатся в /proc как есть. Цикл обязателен: простую команду оболочка
	// заменяет собой через exec, и аргументы теряются
	cmd := exec.Command("sh", append([]string{"-c", "while :; do sleep 1; done"}, args...)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("не удалось запустить процесс: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	pid := strconv.Itoa(cmd.Process.Pid)
	// Между стартом процесса и подменой его образа на sh проходит время,
	// и до этого момента в /proc лежит командная строка тестового бинаря
	for i := 0; i < 200; i++ {
		raw, err := os.ReadFile("/proc/" + pid + "/cmdline")
		if err == nil && strings.Contains(string(raw), "while :;") {
			return pid
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("процесс %s не запустился", pid)
	return pid
}

// printMode вызывает printModeCmdline и проваливает тест на ошибке чтения
func printMode(t *testing.T, pid string) bool {
	t.Helper()
	got, err := printModeCmdline(pid)
	if err != nil {
		t.Fatalf("printModeCmdline(%q): %v", pid, err)
	}
	return got
}

func TestPrintModeCmdline_PrintFlag(t *testing.T) {
	if !printMode(t, startWithArgs(t, "-p")) {
		t.Error("флаг -p в командной строке — сессия неинтерактивная")
	}
	if !printMode(t, startWithArgs(t, "--print")) {
		t.Error("флаг --print в командной строке — сессия неинтерактивная")
	}
}

func TestPrintModeCmdline_Interactive(t *testing.T) {
	if printMode(t, startWithArgs(t)) {
		t.Error("без флага печати сессия интерактивная")
	}
	// Флаг как часть другого аргумента признаком не является
	if printMode(t, startWithArgs(t, "-print")) {
		t.Error("«-print» не флаг печати claude")
	}
}

func TestPrintModeCmdline_NoProcess(t *testing.T) {
	if printMode(t, "") {
		t.Error("без pid режим определить нельзя")
	}
	for _, pid := range []string{"999999999", "../../etc/passwd"} {
		got, err := printModeCmdline(pid)
		if err == nil {
			t.Errorf("pid %q: нечитаемая командная строка должна давать ошибку", pid)
		}
		if got {
			t.Errorf("pid %q: не должен считаться неинтерактивным", pid)
		}
	}
}
