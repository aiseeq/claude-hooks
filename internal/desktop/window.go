package desktop

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	kwinService     = "org.kde.KWin"
	kwinScripting   = "/Scripting"
	kwinScriptIface = "org.kde.kwin.Scripting"
)

// ActivateWindowByPIDs переводит фокус на окно, принадлежащее одному из процессов.
//
// В Wayland нет способа поднять чужое окно снаружи: X11-API там отсутствуют,
// а протокол xdg-activation требует токена, выданного в ответ на действие пользователя.
// Рабочий путь для KDE — выполнить код внутри самого компоновщика: KWin принимает
// JavaScript через D-Bus, а изнутри KWin присваивание workspace.activeWindow
// ограничениями не связано
func ActivateWindowByPIDs(conn *dbus.Conn, pids []int) error {
	if len(pids) == 0 {
		return fmt.Errorf("no pids to match")
	}

	scriptPath, err := writeActivationScript(pids)
	if err != nil {
		return err
	}
	// Временный файл во /tmp: неудалённый остаток безвреден
	defer func() { _ = os.Remove(scriptPath) }()

	// Имя плагина уникально для процесса: параллельные хуки не мешают друг другу
	pluginName := "claude-hooks-activate-" + strconv.Itoa(os.Getpid())
	scripting := conn.Object(kwinService, kwinScripting)

	if err := scripting.Call(kwinScriptIface+".loadScript", 0, scriptPath, pluginName).Err; err != nil {
		return fmt.Errorf("failed to load KWin script: %w", err)
	}
	defer scripting.Call(kwinScriptIface+".unloadScript", 0, pluginName)

	if err := scripting.Call(kwinScriptIface+".start", 0).Err; err != nil {
		return fmt.Errorf("failed to run KWin script: %w", err)
	}

	return nil
}

// writeActivationScript создаёт временный файл с KWin-скриптом.
// KWin читает скрипт с диска, передать его строкой нельзя
func writeActivationScript(pids []int) (string, error) {
	file, err := os.CreateTemp("", "claude-hooks-activate-*.js")
	if err != nil {
		return "", fmt.Errorf("failed to create script file: %w", err)
	}

	// Закрытие после записи проверяется: незаписанный скрипт KWin молча не выполнит
	_, err = file.WriteString(activationScript(pids))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("failed to write script: %w", err)
	}

	return file.Name(), nil
}

// activationScript собирает KWin-скрипт, активирующий первое окно из списка процессов
func activationScript(pids []int) string {
	list := make([]string, 0, len(pids))
	for _, pid := range pids {
		list = append(list, strconv.Itoa(pid))
	}

	return fmt.Sprintf(`var pids = [%s];
var windows = workspace.windowList();
for (var i = 0; i < windows.length; i++) {
    if (pids.indexOf(windows[i].pid) !== -1) {
        workspace.activeWindow = windows[i];
        break;
    }
}
`, strings.Join(list, ", "))
}

// WindowActivationAvailable сообщает, доступен ли KWin для активации окон
func WindowActivationAvailable(conn *dbus.Conn) bool {
	var owner string
	err := conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, kwinService).Store(&owner)
	return err == nil && owner != ""
}
