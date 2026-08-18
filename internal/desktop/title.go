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
// Ошибка означает, что заголовок Konsole поставить не удалось; работу хука
// это не останавливает — решает вызывающий
func SetTerminalTitle(title string) error {
	title = sanitizeTitle(title)
	if title == "" {
		return nil
	}

	if service, session, ok := konsoleSession(); ok {
		return setKonsoleTitle(service, session, title)
	}

	// Заголовок ставится через stderr: stdout у хука и строки статуса занят данными
	fmt.Fprintf(os.Stderr, "\033]0;%s\007", title)
	return nil
}

// konsoleSession сообщает адрес D-Bus сессии Konsole, из которой запущен
// процесс, если это Konsole
func konsoleSession() (service, session string, ok bool) {
	service = os.Getenv("KONSOLE_DBUS_SERVICE")
	session = os.Getenv("KONSOLE_DBUS_SESSION")
	return service, session, service != "" && session != ""
}

// setKonsoleTitle задаёт заголовок сессии Konsole через D-Bus
func setKonsoleTitle(service, session, title string) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return fmt.Errorf("session bus unavailable: %w", err)
	}

	object := conn.Object(service, dbus.ObjectPath(session))

	// Шаблон переживёт перерисовку заголовка, но применяется не сразу:
	// Konsole пересобирает заголовок по событиям сессии. Поэтому текущий
	// заголовок задаётся ещё и напрямую
	if call := object.Call(konsoleSessionIface+".setTabTitleFormat", 0, konsoleLocalTab, title); call.Err != nil {
		return fmt.Errorf("konsole setTabTitleFormat: %w", call.Err)
	}
	if call := object.Call(konsoleSessionIface+".setTitle", 0, konsoleDisplayedTitle, title); call.Err != nil {
		return fmt.Errorf("konsole setTitle: %w", call.Err)
	}

	return nil
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
