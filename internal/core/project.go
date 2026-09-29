package core

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectNameForDir возвращает имя проекта по его каталогу.
//
// Одного последнего сегмента не всегда достаточно: ~/work/projecta/backend и
// ~/work/glint/backend дали бы одинаковое имя. Поэтому у вложенных проектов
// показывается и родительский каталог. Домашний каталог и корень своего имени
// не имеют — для них возвращаются понятные обозначения, а не имя пользователя
func ProjectNameForDir(dir string) string {
	dir = filepath.Clean(dir)

	if dir == string(filepath.Separator) {
		return "/"
	}

	if home, err := os.UserHomeDir(); err == nil {
		home = filepath.Clean(home)
		if dir == home {
			return "~"
		}

		// Внутри домашнего каталога первый уровень — папка вроде work или git,
		// имени проекта она не добавляет
		if relative, relErr := filepath.Rel(home, dir); relErr == nil && !strings.HasPrefix(relative, "..") {
			segments := strings.Split(relative, string(filepath.Separator))
			if len(segments) >= 3 {
				return filepath.Join(segments[len(segments)-2], segments[len(segments)-1])
			}
		}
	}

	if base := filepath.Base(dir); base != "." && base != string(filepath.Separator) {
		return base
	}
	return "unknown"
}
