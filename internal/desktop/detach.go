package desktop

import (
	"fmt"
	"os/exec"
	"syscall"
)

// StartDetached запускает команду в отдельной сессии и сразу возвращает
// управление. Без отдельной сессии фоновый процесс погиб бы вместе с хуком,
// который его запустил. Ввод и вывод отключены: хук и строку статуса Claude
// Code читает по stdout, чужой вывод туда попадать не должен
func StartDetached(cmd *exec.Cmd) error {
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", cmd.Path, err)
	}

	// Процесс переживёт запустившего и ждать его никто не будет: после
	// выхода запустившего его подберёт init
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("failed to release %s: %w", cmd.Path, err)
	}
	return nil
}
