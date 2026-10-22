package skillreload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Сухой прогон: по транскриптам за последние дни посчитать, сколько раз хук
// сказал бы «вызови заново». Хук срабатывает на каждом сообщении
// пользователя и после каждого пакета инструментов — в транскрипте это
// записи type=user. Содержимое SKILL.md во времени собирается из трёх
// источников: снимки вызовов во всех транскриптах (точный текст на момент
// вызова), история git и файл сейчас. Сравнение — по хешу тела, как его видит
// агент, поэтому смена времени файла без правки текста напоминанием не считается

// DryRunResult — итог сухого прогона
type DryRunResult struct {
	Since       time.Time
	Transcripts int
	Loads       int
	Reminders   []DryRunReminder
	// FirstPerLoad — сколько из них первые после вызова: столько было бы
	// при правиле «одно напоминание на вызов», а не на правку
	FirstPerLoad int
	Unmatched    []Load // снимки, не совпавшие ни с одной известной версией файла
	MtimeOnly    int    // скиллы, тронутые после вызова без правки текста
	MtimeChanged int    // скиллы, изменённые после вызова по тексту
	// Problems — что не удалось прочесть (строки транскриптов, история git,
	// файлы скиллов): прогон по ним неполон
	Problems []error
}

// DryRunReminder — одно напоминание, которое хук отправил бы
type DryRunReminder struct {
	Transcript string
	Skill      string
	First      bool // первое напоминание после этого вызова
}

// version — содержимое SKILL.md с какого-то момента
type version struct {
	At   time.Time
	Hash string
}

// transcriptScan — что дал один транскрипт
type transcriptScan struct {
	Path   string
	Loads  []Load
	Events []time.Time
	Broken []error
}

// eventRecord — поля записи для времени события хука
type eventRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
}

// DryRun проходит транскрипты под root, изменённые за days дней
func DryRun(ctx context.Context, root string, days int, now time.Time) (*DryRunResult, error) {
	if days <= 0 {
		return nil, fmt.Errorf("число дней должно быть положительным: %d", days)
	}
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	paths, err := recentTranscripts(root, since)
	if err != nil {
		return nil, err
	}
	scans, err := scanAll(ctx, paths)
	if err != nil {
		return nil, err
	}

	result := &DryRunResult{Since: since, Transcripts: len(scans)}
	timelines := map[string][]version{}
	for _, scan := range scans {
		result.Loads += len(scan.Loads)
		result.Problems = append(result.Problems, scan.Broken...)
		for _, load := range scan.Loads {
			timelines[load.Dir] = append(timelines[load.Dir], version{At: load.At, Hash: bodyHash(load.Snapshot)})
		}
	}

	known := map[string]map[string]bool{}
	for _, dir := range sortedKeys(timelines) {
		external, problems := fileVersions(ctx, dir)
		result.Problems = append(result.Problems, problems...)
		known[dir] = map[string]bool{}
		for _, v := range external {
			known[dir][v.Hash] = true
		}
		timeline := append(timelines[dir], external...)
		sort.SliceStable(timeline, func(i, j int) bool { return timeline[i].At.Before(timeline[j].At) })
		timelines[dir] = timeline
	}

	for _, scan := range scans {
		for _, reminder := range simulate(scan, timelines, since) {
			result.Reminders = append(result.Reminders, reminder)
			if reminder.First {
				result.FirstPerLoad++
			}
		}
		for _, load := range scan.Loads {
			if !known[load.Dir][bodyHash(load.Snapshot)] {
				result.Unmatched = append(result.Unmatched, load)
			}
		}
		mtimeOnly, changed, problems := mtimeStats(scan)
		result.MtimeOnly += mtimeOnly
		result.MtimeChanged += changed
		result.Problems = append(result.Problems, problems...)
	}
	return result, nil
}

// recentTranscripts — jsonl-файлы под root, изменённые после since,
// вместе с транскриптами субагентов
func recentTranscripts(root string, since time.Time) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("cannot stat transcript %s: %w", path, err)
		}
		if info.ModTime().After(since) {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

// scanAll читает транскрипты параллельно
func scanAll(ctx context.Context, paths []string) ([]transcriptScan, error) {
	scans := make([]transcriptScan, len(paths))
	errs := make([]error, len(paths))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				scans[i], errs[i] = scanForDryRun(paths[i])
			}
		}()
	}
	for i := range paths {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return scans, nil
}

// scanForDryRun собирает вызовы скиллов и моменты событий хука
func scanForDryRun(path string) (transcriptScan, error) {
	scan := transcriptScan{Path: path}
	userMarker := []byte(`"type":"user"`)
	_, _, err := forEachLine(path, 0, time.Time{}, func(line []byte, at int64) error {
		load, ok, err := parseLoad(line)
		switch {
		case err != nil:
			scan.Broken = append(scan.Broken, fmt.Errorf("%s at byte %d: %w", path, at, err))
			return nil
		case ok:
			scan.Loads = append(scan.Loads, load)
			return nil
		case !bytes.Contains(line, userMarker):
			return nil
		}
		event, isEvent, err := parseEvent(line)
		switch {
		case err != nil:
			scan.Broken = append(scan.Broken, fmt.Errorf("%s at byte %d: %w", path, at, err))
		case isEvent:
			scan.Events = append(scan.Events, event)
		}
		return nil
	})
	sort.Slice(scan.Events, func(i, j int) bool { return scan.Events[i].Before(scan.Events[j]) })
	sort.SliceStable(scan.Loads, func(i, j int) bool { return scan.Loads[i].At.Before(scan.Loads[j].At) })
	return scan, err
}

// parseEvent — время записи type=user: в этот момент срабатывает хук.
// isEvent ложно для записей другого типа, где "type":"user" лишь в тексте
func parseEvent(line []byte) (at time.Time, isEvent bool, err error) {
	var record eventRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return time.Time{}, false, fmt.Errorf("record is not JSON: %w", err)
	}
	if record.Type != "user" {
		return time.Time{}, false, nil
	}
	at, err = time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("user record has no time: %w", err)
	}
	return at, true, nil
}

// simulate проигрывает события транскрипта: после вызова скилла каждое
// событие сверяет версию файла на тот момент со снимком. Агент в прогоне
// скилл заново не вызывает — счёт сверху
func simulate(scan transcriptScan, timelines map[string][]version, since time.Time) []DryRunReminder {
	type seen struct {
		hash     string
		notified string
	}
	current := map[string]*seen{}
	var reminders []DryRunReminder
	next := 0
	for _, event := range scan.Events {
		for next < len(scan.Loads) && !scan.Loads[next].At.After(event) {
			load := scan.Loads[next]
			current[load.Dir] = &seen{hash: bodyHash(load.Snapshot)}
			next++
		}
		if event.Before(since) {
			continue
		}
		for _, dir := range sortedKeys(current) {
			s := current[dir]
			v, ok := versionAt(timelines[dir], event)
			if !ok || v.Hash == s.hash || v.Hash == s.notified {
				continue
			}
			reminders = append(reminders, DryRunReminder{
				Transcript: scan.Path,
				Skill:      skillNameFromDir(dir),
				First:      s.notified == "",
			})
			s.notified = v.Hash
		}
	}
	return reminders
}

// versionAt — последняя известная версия файла не позже момента at
func versionAt(timeline []version, at time.Time) (version, bool) {
	i := sort.Search(len(timeline), func(i int) bool { return timeline[i].At.After(at) })
	if i == 0 {
		return version{}, false
	}
	return timeline[i-1], true
}

// mtimeStats сравнивает последний вызов каждого скилла транскрипта с файлом
// сейчас: сколько тронуто после вызова без правки текста и сколько изменено.
// Отсутствующий файл не считается
func mtimeStats(scan transcriptScan) (mtimeOnly, changed int, problems []error) {
	last := map[string]Load{}
	for _, load := range scan.Loads {
		last[load.Dir] = load
	}
	for _, dir := range sortedKeys(last) {
		load := last[dir]
		file, err := readSkillFile(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		if !file.ModTime.After(load.At) {
			continue
		}
		if renderBody(file.Raw, dir) == load.Snapshot {
			mtimeOnly++
		} else {
			changed++
		}
	}
	return mtimeOnly, changed, problems
}

// bodyHash — хеш тела в виде снимка
func bodyHash(body string) string {
	return contentHash([]byte(body))
}

// fileVersions — версии SKILL.md из истории git и файл сейчас. Каталог вне
// git (встроенные скиллы во временном каталоге) — только файл сейчас и
// проблема в итоге
func fileVersions(ctx context.Context, dir string) ([]version, []error) {
	var versions []version
	var problems []error
	file, err := readSkillFile(dir)
	switch {
	case err == nil:
		versions = append(versions, version{At: file.ModTime, Hash: bodyHash(renderBody(file.Raw, dir))})
	case !errors.Is(err, fs.ErrNotExist):
		problems = append(problems, fmt.Errorf("%s: %w", dir, err))
	}

	git, path, err := gitFor(dir)
	if err != nil {
		return versions, append(problems, fmt.Errorf("%s: no git history: %w", dir, err))
	}
	out, err := git(ctx, "log", "--format=%H %ct", "--", path)
	if err != nil {
		return versions, append(problems, fmt.Errorf("%s: %w", dir, err))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		commit, stamp, _ := strings.Cut(line, " ")
		seconds, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: git log line %q: %w", dir, line, err))
			continue
		}
		// Коммит, удаливший файл, тоже в log, а show по нему падает
		if exists, err := git(ctx, "ls-tree", "--name-only", commit, "--", path); err != nil || len(bytes.TrimSpace(exists)) == 0 {
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", dir, err))
			}
			continue
		}
		raw, err := git(ctx, "show", commit+":"+path)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", dir, err))
			continue
		}
		versions = append(versions, version{At: time.Unix(seconds, 0), Hash: bodyHash(renderBody(raw, dir))})
	}
	return versions, problems
}

type gitRunner func(ctx context.Context, args ...string) ([]byte, error)

// gitFor — git репозитория скилла и путь SKILL.md в нём. Общие скиллы
// (~/.claude/skills) лежат в голом репо ~/.claude-config.git с деревом в ~
func gitFor(dir string) (gitRunner, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("cannot resolve home dir: %w", err)
	}
	userSkills := filepath.Join(home, ".claude", "skills") + string(filepath.Separator)
	if strings.HasPrefix(dir, userSkills) {
		rel, err := filepath.Rel(home, filepath.Join(dir, skillFileName))
		if err != nil {
			return nil, "", fmt.Errorf("skill path outside home: %w", err)
		}
		gitDir := filepath.Join(home, ".claude-config.git")
		return func(ctx context.Context, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + gitDir, "--work-tree=" + home}, args...)...)
			cmd.Dir = home
			return output(cmd)
		}, rel, nil
	}

	top, err := output(exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel"))
	if err != nil {
		return nil, "", err
	}
	root := strings.TrimSpace(string(top))
	rel, err := filepath.Rel(root, filepath.Join(dir, skillFileName))
	if err != nil {
		return nil, "", fmt.Errorf("skill path outside its repo: %w", err)
	}
	return func(ctx context.Context, args ...string) ([]byte, error) {
		return output(exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...))
	}, rel, nil
}

// output запускает команду и возвращает stdout; stderr — в тексте ошибки
func output(cmd *exec.Cmd) ([]byte, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// WriteDryRun печатает итог сухого прогона
func WriteDryRun(w io.Writer, r *DryRunResult) {
	fmt.Fprintf(w, "Транскриптов с %s: %d, вызовов скиллов: %d\n", localTime(r.Since), r.Transcripts, r.Loads)
	fmt.Fprintf(w, "Напоминаний «вызови заново»: %d, из них первых после вызова: %d (агент в прогоне скилл заново не вызывает)\n",
		len(r.Reminders), r.FirstPerLoad)

	bySkill := map[string]int{}
	firstBySkill := map[string]int{}
	byTranscript := map[string]int{}
	for _, reminder := range r.Reminders {
		bySkill[reminder.Skill]++
		byTranscript[reminder.Transcript]++
		if reminder.First {
			firstBySkill[reminder.Skill]++
		}
	}
	fmt.Fprintf(w, "В транскриптах: %d\n", len(byTranscript))
	fmt.Fprintln(w, "По скиллам (всего / первых после вызова):")
	for _, name := range sortedByCount(bySkill) {
		fmt.Fprintf(w, "  %4d / %3d  %s\n", bySkill[name], firstBySkill[name], name)
	}
	fmt.Fprintln(w, "Больше всего в транскриптах:")
	for i, path := range sortedByCount(byTranscript) {
		if i == 10 {
			break
		}
		fmt.Fprintf(w, "  %4d  %s\n", byTranscript[path], path)
	}
	fmt.Fprintf(w, "Последний вызов скилла сейчас: тронут после вызова без правки текста — %d, изменён по тексту — %d\n", r.MtimeOnly, r.MtimeChanged)

	unmatched := map[string]int{}
	for _, load := range r.Unmatched {
		unmatched[skillNameFromDir(load.Dir)]++
	}
	fmt.Fprintf(w, "Снимков без совпадения с версией из git или файлом сейчас: %d из %d\n", len(r.Unmatched), r.Loads)
	for _, name := range sortedByCount(unmatched) {
		fmt.Fprintf(w, "  %4d  %s\n", unmatched[name], name)
	}

	fmt.Fprintf(w, "Не прочитано: %d\n", len(r.Problems))
	for i, problem := range r.Problems {
		if i == 10 {
			fmt.Fprintf(w, "  … ещё %d\n", len(r.Problems)-i)
			break
		}
		fmt.Fprintf(w, "  %v\n", problem)
	}
}

// sortedByCount — ключи по убыванию счёта, при равенстве по имени
func sortedByCount(counts map[string]int) []string {
	keys := sortedKeys(counts)
	sort.SliceStable(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
	return keys
}

// sortedKeys — ключи отображения по порядку: итог не зависит от порядка обхода
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
