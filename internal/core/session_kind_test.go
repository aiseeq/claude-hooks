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

func TestPrintModeCmdline_PrintFlag(t *testing.T) {
	if !printModeCmdline(startWithArgs(t, "-p")) {
		t.Error("флаг -p в командной строке — сессия неинтерактивная")
	}
	if !printModeCmdline(startWithArgs(t, "--print")) {
		t.Error("флаг --print в командной строке — сессия неинтерактивная")
	}
}

func TestPrintModeCmdline_Interactive(t *testing.T) {
	if printModeCmdline(startWithArgs(t)) {
		t.Error("без флага печати сессия интерактивная")
	}
	// Флаг как часть другого аргумента признаком не является
	if printModeCmdline(startWithArgs(t, "-print")) {
		t.Error("«-print» не флаг печати claude")
	}
}

func TestPrintModeCmdline_NoProcess(t *testing.T) {
	if printModeCmdline("") {
		t.Error("без pid режим определить нельзя")
	}
	if printModeCmdline("999999999") {
		t.Error("несуществующий процесс не должен считаться неинтерактивным")
	}
	if printModeCmdline("../../etc/passwd") {
		t.Error("pid из окружения не должен уводить чтение из /proc")
	}
}
