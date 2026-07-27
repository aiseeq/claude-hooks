package desktop

import (
	"os"
	"strconv"
	"strings"
)

// maxAncestors ограничивает подъём по дереву процессов: цепочка от хука
// до эмулятора терминала короткая, а зацикливаться на битом /proc нельзя
const maxAncestors = 16

// ProcessAncestors возвращает PID процесса и всех его предков, начиная с самого процесса.
// Окно терминала принадлежит одному из них: какому именно — решает оконный менеджер,
// поэтому список передаётся целиком и не требует знания конкретных эмуляторов терминала
func ProcessAncestors(pid int) []int {
	ancestors := make([]int, 0, maxAncestors)
	seen := make(map[int]bool, maxAncestors)

	for len(ancestors) < maxAncestors && pid > 1 && !seen[pid] {
		seen[pid] = true
		ancestors = append(ancestors, pid)

		parent, ok := parentPID(pid)
		if !ok {
			break
		}
		pid = parent
	}

	return ancestors
}

// parentPID читает PPid процесса из /proc/<pid>/stat
func parentPID(pid int) (int, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}

	// Формат: pid (comm) state ppid ... — comm может содержать пробелы и скобки,
	// поэтому разбор начинается после последней закрывающей скобки
	stat := string(data)
	end := strings.LastIndex(stat, ")")
	if end == -1 || end+2 >= len(stat) {
		return 0, false
	}

	fields := strings.Fields(stat[end+2:])
	if len(fields) < 2 {
		return 0, false
	}

	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}

	return parent, true
}
