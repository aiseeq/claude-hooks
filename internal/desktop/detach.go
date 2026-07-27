package desktop

import (
	"os/exec"
	"syscall"
)

// detachProcess переводит команду в отдельную сессию.
// Без этого фоновый наблюдатель погибнет вместе с хуком, который его запустил
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
