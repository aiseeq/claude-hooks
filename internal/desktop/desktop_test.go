package desktop

import (
	"os"
	"strings"
	"testing"
)

func TestProcessAncestors(t *testing.T) {
	ancestors := ProcessAncestors(os.Getpid())

	if len(ancestors) == 0 {
		t.Fatal("список предков не должен быть пустым")
	}
	if ancestors[0] != os.Getpid() {
		t.Errorf("первым идёт сам процесс: получено %d, ожидалось %d", ancestors[0], os.Getpid())
	}
	if len(ancestors) > 1 && ancestors[1] != os.Getppid() {
		t.Errorf("вторым идёт родитель: получено %d, ожидалось %d", ancestors[1], os.Getppid())
	}

	seen := make(map[int]bool, len(ancestors))
	for _, pid := range ancestors {
		if seen[pid] {
			t.Fatalf("цепочка содержит повтор PID %d", pid)
		}
		seen[pid] = true
	}
}

func TestProcessAncestors_UnknownProcess(t *testing.T) {
	// PID заведомо за пределами диапазона: цепочка обрывается без паники
	if got := ProcessAncestors(1 << 30); len(got) > 1 {
		t.Errorf("для несуществующего процесса ожидался обрыв цепочки, получено %v", got)
	}
}

func TestActivationScript(t *testing.T) {
	script := activationScript([]int{101, 202})

	if !strings.Contains(script, "var pids = [101, 202];") {
		t.Errorf("список процессов не подставлен: %s", script)
	}
	if !strings.Contains(script, "workspace.activeWindow = windows[i]") {
		t.Errorf("скрипт не активирует окно: %s", script)
	}
	// Присваивание activeWindow — единственный способ передать фокус из KWin 6
	if strings.Contains(script, "activateWindow(") {
		t.Error("метод activateWindow() в KWin 6 отсутствует")
	}
}

func TestWriteActivationScript(t *testing.T) {
	path, err := writeActivationScript([]int{7})
	if err != nil {
		t.Fatalf("не удалось записать скрипт: %v", err)
	}
	defer os.Remove(path)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не удалось прочитать скрипт: %v", err)
	}
	if !strings.Contains(string(content), "var pids = [7];") {
		t.Errorf("содержимое скрипта неверно: %s", content)
	}
}

func TestActivateWindowByPIDs_NoPIDs(t *testing.T) {
	if err := ActivateWindowByPIDs(nil, nil); err == nil {
		t.Error("без списка процессов активация невозможна")
	}
}

func TestIntsRoundTrip(t *testing.T) {
	values := []int{1, 42, 1000}

	encoded := joinInts(values)
	if encoded != "1,42,1000" {
		t.Errorf("получено %q", encoded)
	}

	decoded := ParseInts(encoded)
	if len(decoded) != len(values) {
		t.Fatalf("получено %v, ожидалось %v", decoded, values)
	}
	for i := range values {
		if decoded[i] != values[i] {
			t.Errorf("позиция %d: получено %d, ожидалось %d", i, decoded[i], values[i])
		}
	}
}

func TestParseInts_IgnoresGarbage(t *testing.T) {
	if got := ParseInts("1, x, 3"); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("нечисловые элементы должны пропускаться, получено %v", got)
	}
	if got := ParseInts(""); got != nil {
		t.Errorf("пустая строка даёт пустой список, получено %v", got)
	}
}
