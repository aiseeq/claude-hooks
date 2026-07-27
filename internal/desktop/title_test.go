package desktop

import "testing"

func TestSanitizeTitle(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		expected string
	}{
		{name: "обычный заголовок", title: "🔵 DEMO · main", expected: "🔵 DEMO · main"},
		// Konsole принимает %d и %n за подстановки каталога и имени сессии
		{name: "подстановки Konsole", title: "100% DONE · %d", expected: "100 DONE · d"},
		// Перевод строки оборвал бы escape-последовательность OSC
		{name: "управляющие символы", title: "DEMO\nвторая строка", expected: "DEMO вторая строка"},
		{name: "пустой заголовок", title: "   ", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeTitle(tt.title); got != tt.expected {
				t.Errorf("sanitizeTitle(%q) = %q, ожидалось %q", tt.title, got, tt.expected)
			}
		})
	}
}

func TestSetKonsoleTitleWithoutKonsole(t *testing.T) {
	// Вне Konsole нужен запасной путь через OSC, а не попытка вызова D-Bus
	t.Setenv("KONSOLE_DBUS_SERVICE", "")
	t.Setenv("KONSOLE_DBUS_SESSION", "")

	if setKonsoleTitle("DEMO") {
		t.Error("без переменных Konsole заголовок не может быть установлен через D-Bus")
	}
}
