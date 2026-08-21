package desktop

import (
	"os"
	"strings"
	"testing"
)

func TestProcessAncestors(t *testing.T) {
	ancestors, err := ProcessAncestors(os.Getpid())
	if err != nil {
		t.Fatalf("цепочка живого процесса должна читаться целиком: %v", err)
	}

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
	// PID заведомо за пределами диапазона: цепочка обрывается без паники,
	// а обрыв виден по ошибке
	got, err := ProcessAncestors(1 << 30)
	if err == nil {
		t.Error("для несуществующего процесса ожидалась ошибка чтения /proc")
	}
	if len(got) > 1 {
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
	defer func() { _ = os.Remove(path) }()

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

	decoded, err := ParseInts(encoded)
	if err != nil {
		t.Fatalf("ParseInts(%q): %v", encoded, err)
	}
	if len(decoded) != len(values) {
		t.Fatalf("получено %v, ожидалось %v", decoded, values)
	}
	for i := range values {
		if decoded[i] != values[i] {
			t.Errorf("позиция %d: получено %d, ожидалось %d", i, decoded[i], values[i])
		}
	}
}

func TestParseInts_RejectsGarbage(t *testing.T) {
	if got, err := ParseInts("1, x, 3"); err == nil {
		t.Errorf("нечисловой элемент должен давать ошибку, получено %v", got)
	}
	got, err := ParseInts("")
	if err != nil {
		t.Errorf("пустая строка не ошибка: %v", err)
	}
	if got != nil {
		t.Errorf("пустая строка даёт пустой список, получено %v", got)
	}
}
