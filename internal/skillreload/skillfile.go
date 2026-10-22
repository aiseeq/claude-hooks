package skillreload

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// skillFileName — файл скилла в его каталоге. Соседние файлы скилл
// подгружает инструментом Read, а за прочитанными файлами Claude Code следит
// сам (вложение edited_text_file); снимком остаётся только SKILL.md
const skillFileName = "SKILL.md"

// skillFile — SKILL.md на диске сейчас
type skillFile struct {
	Path    string
	Raw     []byte
	ModTime time.Time
}

// readSkillFile читает SKILL.md каталога скилла
func readSkillFile(dir string) (skillFile, error) {
	path := filepath.Join(dir, skillFileName)
	info, err := os.Stat(path)
	if err != nil {
		return skillFile{}, fmt.Errorf("cannot stat skill file: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return skillFile{}, fmt.Errorf("cannot read skill file: %w", err)
	}
	return skillFile{Path: path, Raw: raw, ModTime: info.ModTime()}, nil
}

// contentHash — хеш содержимого файла: по нему, а не по времени изменения,
// видно правку. Время меняют и Сон, и выкладка конфигурации без правки текста
func contentHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// renderBody приводит SKILL.md к виду снимка в контексте: без шапки,
// ${CLAUDE_SKILL_DIR} заменён каталогом, пробелы по краям сняты
func renderBody(raw []byte, dir string) string {
	_, body := splitFrontmatter(string(raw))
	body = strings.ReplaceAll(body, "${CLAUDE_SKILL_DIR}", dir)
	return strings.TrimSpace(body)
}

// splitFrontmatter отделяет шапку YAML (между строками ---) от тела
func splitFrontmatter(text string) (string, string) {
	text = strings.TrimPrefix(text, "\uFEFF")
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return "", text
	}
	_, rest, _ := strings.Cut(text, "\n")
	for offset := 0; offset < len(rest); {
		end := strings.IndexByte(rest[offset:], '\n')
		line := rest[offset:]
		next := len(rest)
		if end >= 0 {
			line = rest[offset : offset+end]
			next = offset + end + 1
		}
		if strings.TrimRight(line, "\r") == "---" {
			return rest[:offset], rest[next:]
		}
		offset = next
	}
	// Шапка не закрыта: телом считается весь файл, как без шапки
	return "", text
}

// skillName — имя, под которым скилл вызывают: name из шапки, иначе имя
// каталога; у скиллов плагинов — с приставкой плагина
func skillName(dir string, raw []byte) string {
	name := filepath.Base(dir)
	frontmatter, _ := splitFrontmatter(string(raw))
	for _, line := range strings.Split(frontmatter, "\n") {
		key, value, ok := strings.Cut(strings.TrimRight(line, "\r"), ":")
		if ok && strings.TrimSpace(key) == "name" {
			if v := strings.Trim(strings.TrimSpace(value), `"'`); v != "" {
				name = v
			}
			break
		}
	}
	if plugin := pluginName(dir); plugin != "" && !strings.Contains(name, ":") {
		return plugin + ":" + name
	}
	return name
}

// pluginName — имя плагина для каталога вида
// ~/.claude/plugins/cache/<маркет>/<плагин>/<версия>/skills/<скилл>
func pluginName(dir string) string {
	parts := strings.Split(filepath.ToSlash(dir), "/")
	for i := 0; i+6 < len(parts); i++ {
		if parts[i] == "plugins" && parts[i+1] == "cache" && parts[i+5] == "skills" {
			return parts[i+3]
		}
	}
	return ""
}
