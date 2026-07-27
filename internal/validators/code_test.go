package validators

import (
	"strings"
	"testing"
)

func TestStripNonCode(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "строковый литерал",
			content:  `msg := "panic(here)"`,
			expected: `msg := "           "`,
		},
		{
			name:     "однострочный комментарий",
			content:  "code() // panic(x)",
			expected: "code()            ",
		},
		{
			name:     "многострочный комментарий",
			content:  "a()\n/* panic(x)\nos.Exit(1) */\nb()",
			expected: "a()\n           \n             \nb()",
		},
		{
			name:     "многострочная строка Go",
			content:  "s := `\nos.Exit(1)\n`",
			expected: "s := `\n          \n`",
		},
		{
			name:     "экранированная кавычка внутри строки",
			content:  `s := "a\"b"`,
			expected: `s := "    "`,
		},
		{
			name:     "код сохраняется полностью",
			content:  "os.Exit(1)",
			expected: "os.Exit(1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripNonCode(tt.content)
			if got != tt.expected {
				t.Errorf("получено %q, ожидалось %q", got, tt.expected)
			}
		})
	}
}

// Позиции нарушений считаются по очищенному тексту, поэтому длины должны совпадать
func TestStripNonCode_PreservesLayout(t *testing.T) {
	content := "package main\n\nfunc main() { // запуск\n\ts := \"текст\"\n}\n"
	stripped := StripNonCode(content)

	if len(strings.Split(stripped, "\n")) != len(strings.Split(content, "\n")) {
		t.Fatal("количество строк изменилось")
	}
	for i, line := range strings.Split(content, "\n") {
		if got := len(strings.Split(stripped, "\n")[i]); got != len(line) {
			t.Errorf("строка %d: длина %d, ожидалась %d", i+1, got, len(line))
		}
	}
}
