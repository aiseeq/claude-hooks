package desktop

import (
	"fmt"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	konsoleSessionIface = "org.kde.konsole.Session"

	// konsoleLocalTab — контекст формата заголовка для локальной сессии.
	// У удалённой (ssh) сессии формат свой, но хуки работают локально
	konsoleLocalTab = int32(0)

	// konsoleDisplayedTitle — роль показываемого заголовка, а не имени профиля
	konsoleDisplayedTitle = int32(1)
)

// SetTerminalTitle меняет заголовок окна терминала.
//
// Konsole собирает заголовок по шаблону («%d : %n» по умолчанию) и escape-
// последовательность OSC при этом игнорирует, поэтому шаблон заменяется целиком
// через D-Bus. Для прочих терминалов остаётся OSC — его понимают почти все.
// Ошибки не возвращаются: заголовок окна не повод ломать работу хука
func SetTerminalTitle(title string) {
	title = sanitizeTitle(title)
	if title == "" {
		return
	}

	if setKonsoleTitle(title) {
		return
	}

	// Заголовок ставится через stderr: stdout у хука и строки статуса занят данными
	fmt.Fprintf(os.Stderr, "\033]0;%s\007", title)
}

// setKonsoleTitle задаёт заголовок сессии Konsole и сообщает, удалось ли это
func setKonsoleTitle(title string) bool {
	service := os.Getenv("KONSOLE_DBUS_SERVICE")
	session := os.Getenv("KONSOLE_DBUS_SESSION")
	if service == "" || session == "" {
		return false
	}

	conn, err := dbus.SessionBus()
	if err != nil {
		return false
	}

	object := conn.Object(service, dbus.ObjectPath(session))

	// Шаблон переживает перерисовку заголовка, но применяется не сразу:
	// Konsole пересобирает заголовок по событиям сессии. Поэтому текущий
	// заголовок задаётся ещё и напрямую
	if call := object.Call(konsoleSessionIface+".setTabTitleFormat", 0, konsoleLocalTab, title); call.Err != nil {
		return false
	}
	object.Call(konsoleSessionIface+".setTitle", 0, konsoleDisplayedTitle, title)

	return true
}

// sanitizeTitle убирает из заголовка то, что исказит его при выводе:
// перевод строки обрывает escape-последовательность, а знак процента
// Konsole принимает за начало подстановки вроде %d или %n
func sanitizeTitle(title string) string {
	title = strings.Map(func(r rune) rune {
		if r == '%' {
			return -1
		}
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, title)

	return strings.TrimSpace(title)
}
