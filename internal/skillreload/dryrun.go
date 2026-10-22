package skillreload

import (
	"bytes"
	"context"
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

// Сухой прогон: по транскриптам за последние дни проиграть хук и посчитать,
// сколько правок он выдал бы и в каком виде. Хук срабатывает на каждом
// сообщении пользователя и после каждого пакета инструментов — в транскрипте
// это записи type=user. Тексты файлов во времени собираются из трёх
// источников: тексты в контексте во всех транскриптах (вызовы скиллов,
// списки инструкций — точный текст на тот момент), история git и файл сейчас.
// Заметки Claude Code в транскриптах настоящие: прогон видит, какие правки
// показал он сам

// DryRunResult — итог сухого прогона
type DryRunResult struct {
	Since       time.Time
	Transcripts int
	Loads       int // загрузок скиллов и файлов инструкций
	Notices     []DryRunNotice
	// HandedToClaude — правки, которые хук оставил Claude Code, и тот их
	// показал заметкой
	HandedToClaude int
	// Duplicates — выдачи хука, после которых до следующего события пришла
	// и заметка Claude Code о том же файле
	Duplicates int
	// RenderMismatch — загрузки, где текст в контексте не совпал с файлом,
	// не менявшимся после загрузки
	RenderMismatch []RenderMismatch
	// Problems — что не удалось прочесть (строки транскриптов, история git,
	// файлы): прогон по ним неполон
	Problems []error
}

// DryRunNotice — одна выдача, которую хук сделал бы
type DryRunNotice struct {
	Transcript string
	At         time.Time
	Notice     Notice
}

// version — текст файла с какого-то момента
type version struct {
	At   time.Time
	Text string
}

// RenderMismatch — загрузка с текстом не как на диске. Stale — Claude Code
// положил прежнюю версию файла (она есть в истории); иначе — подстановка,
// которой хук не повторяет
type RenderMismatch struct {
	Path  string
	Stale bool
}

// transcriptScan — что дал один транскрипт
type transcriptScan struct {
	Path   string
	Events []transcriptEvent
	Broken []error
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
	timelines, kinds := collectVersions(scans)
	for _, path := range sortedKeys(kinds) {
		external, problems := fileVersions(ctx, kinds[path], path)
		result.Problems = append(result.Problems, problems...)
		timelines[path] = mergeTimeline(external, timelines[path])
	}
	for _, scan := range scans {
		result.Problems = append(result.Problems, scan.Broken...)
		replay(scan, timelines, since, result)
	}
	return result, nil
}

// collectVersions — тексты файлов из загрузок во всех транскриптах. Снимок,
// подложенный сжатием, — текст прежнего вызова, версией на момент сжатия он
// не служит
func collectVersions(scans []transcriptScan) (map[string][]version, map[string]string) {
	timelines := map[string][]version{}
	kinds := map[string]string{}
	for _, scan := range scans {
		for _, ev := range scan.Events {
			switch ev.Kind {
			case evSkillLoad:
				kinds[ev.Path] = kindSkill
				timelines[ev.Path] = append(timelines[ev.Path], version{At: ev.At, Text: ev.Text})
			case evMemoryLoad:
				kinds[ev.Path] = kindMemory
				timelines[ev.Path] = append(timelines[ev.Path], version{At: ev.At, Text: renderMemory([]byte(ev.Text))})
			}
		}
	}
	return timelines, kinds
}

// replay проигрывает транскрипт тем же автоматом, что и хук, с файлами по
// истории на момент каждого события
func replay(scan transcriptScan, timelines map[string][]version, since time.Time, result *DryRunResult) {
	state := newSessionState()
	var now time.Time
	view := func(_, path string) (fileView, error) {
		v, ok := versionAt(timelines[path], now)
		if !ok {
			return fileView{}, nil
		}
		return fileView{Exists: true, Text: v.Text, ModTime: v.At}, nil
	}
	t := tracker{state: &state, store: memStore{}, view: view}
	announced := map[string]bool{} // файлы выдачи с прошлого события хука

	for _, ev := range scan.Events {
		now = ev.At
		state.Offset = ev.Offset
		switch ev.Kind {
		case evHook:
			announced = map[string]bool{}
			notices, errs := t.notices(ev.At)
			result.Problems = append(result.Problems, errs...)
			if ev.At.Before(since) {
				continue
			}
			for i := range notices {
				announced[notices[i].Path] = true
				result.Notices = append(result.Notices, DryRunNotice{Transcript: scan.Path, At: ev.At, Notice: notices[i]})
			}
			continue
		case evNote:
			countNote(state.Files[ev.Path], ev, announced[ev.Path], since, result)
		case evSkillLoad, evMemoryLoad:
			result.Loads++
			if err := checkRender(ev, timelines[ev.Path], result); err != nil {
				result.Problems = append(result.Problems, err)
			}
		}
		if err := t.apply(ev); err != nil {
			result.Problems = append(result.Problems, fmt.Errorf("%s: %w", scan.Path, err))
		}
	}
}

// countNote считает заметку Claude Code: правку, которую хук ему оставил, и
// правку, которую хук уже выдал с прошлого события (дубль)
func countNote(w *watched, ev transcriptEvent, announced bool, since time.Time, result *DryRunResult) {
	if !ev.Delivered || ev.At.Before(since) {
		return
	}
	if w != nil && w.Deferred != "" {
		result.HandedToClaude++
	}
	if announced {
		result.Duplicates++
	}
}

// checkRender сверяет текст загрузки с файлом на диске, не менявшимся после
// неё: расхождение — прежняя версия файла из кеша Claude Code или ошибка в
// повторении подстановок
func checkRender(ev transcriptEvent, timeline []version, result *DryRunResult) error {
	kind, text := kindSkill, ev.Text
	if ev.Kind == evMemoryLoad {
		kind, text = kindMemory, renderMemory([]byte(ev.Text))
	}
	v, err := diskView(kind, ev.Path)
	if err != nil {
		return fmt.Errorf("render check %s: %w", ev.Path, err)
	}
	if !v.Exists || v.ModTime.After(ev.At) || v.Text == text {
		return nil
	}
	stale := false
	for _, old := range timeline {
		if old.At.Before(v.ModTime) && old.Text == text {
			stale = true
			break
		}
	}
	result.RenderMismatch = append(result.RenderMismatch, RenderMismatch{Path: ev.Path, Stale: stale})
	return nil
}

// mergeTimeline — история файла: версии из git и с диска, к ним тексты
// загрузок, которых раньше не было. Загрузка уже бывшего текста — прежняя
// версия из кеша Claude Code, а не откат файла: иначе она переставила бы
// историю назад и дала ложные правки всем сессиям
func mergeTimeline(external, loads []version) []version {
	timeline := append([]version(nil), external...)
	sort.SliceStable(timeline, func(i, j int) bool { return timeline[i].At.Before(timeline[j].At) })
	sorted := append([]version(nil), loads...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	for _, load := range sorted {
		seen := false
		for _, v := range timeline {
			if !v.At.After(load.At) && v.Text == load.Text {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		i := sort.Search(len(timeline), func(i int) bool { return timeline[i].At.After(load.At) })
		timeline = append(timeline, version{})
		copy(timeline[i+1:], timeline[i:])
		timeline[i] = load
	}
	return timeline
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
				scan, err := scanTranscript(paths[i], 0, time.Time{}, true)
				scans[i] = transcriptScan{Path: paths[i], Events: scan.Events, Broken: scan.Broken}
				errs[i] = err
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

// versionAt — последняя известная версия файла не позже момента at
func versionAt(timeline []version, at time.Time) (version, bool) {
	i := sort.Search(len(timeline), func(i int) bool { return timeline[i].At.After(at) })
	if i == 0 {
		return version{}, false
	}
	return timeline[i-1], true
}

// fileVersions — версии файла из истории git и файл сейчас. Файл вне git
// (встроенные скиллы во временном каталоге, ~/work/CLAUDE.md) — только файл
// сейчас и проблема в итоге
func fileVersions(ctx context.Context, kind, path string) ([]version, []error) {
	var versions []version
	var problems []error
	view, err := diskView(kind, path)
	switch {
	case err != nil:
		problems = append(problems, err)
	case view.Exists:
		versions = append(versions, version{At: view.ModTime, Text: view.Text})
	}

	git, rel, err := gitFor(path)
	if err != nil {
		return versions, append(problems, fmt.Errorf("%s: no git history: %w", path, err))
	}
	out, err := git(ctx, "log", "--format=%H %ct", "--", rel)
	if err != nil {
		return versions, append(problems, fmt.Errorf("%s: %w", path, err))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		commit, stamp, _ := strings.Cut(line, " ")
		seconds, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: git log line %q: %w", path, line, err))
			continue
		}
		// Коммит, удаливший файл, тоже в log, а show по нему падает
		if exists, err := git(ctx, "ls-tree", "--name-only", commit, "--", rel); err != nil || len(bytes.TrimSpace(exists)) == 0 {
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", path, err))
			}
			continue
		}
		raw, err := git(ctx, "show", commit+":"+rel)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, err))
			continue
		}
		text, _ := render(kind, path, raw)
		versions = append(versions, version{At: time.Unix(seconds, 0), Text: text})
	}
	return versions, problems
}

type gitRunner func(ctx context.Context, args ...string) ([]byte, error)

// gitFor — git репозитория файла и путь файла в нём. Общие инструкции и
// скиллы (~/.claude) лежат в голом репо ~/.claude-config.git с деревом в ~
func gitFor(path string) (gitRunner, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", fmt.Errorf("cannot resolve home dir: %w", err)
	}
	userConfig := filepath.Join(home, ".claude") + string(filepath.Separator)
	if strings.HasPrefix(path, userConfig) {
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return nil, "", fmt.Errorf("file outside home: %w", err)
		}
		gitDir := filepath.Join(home, ".claude-config.git")
		return func(ctx context.Context, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + gitDir, "--work-tree=" + home}, args...)...)
			cmd.Dir = home
			return output(cmd)
		}, rel, nil
	}

	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, "", err
	}
	top, err := output(exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel"))
	if err != nil {
		return nil, "", err
	}
	root := strings.TrimSpace(string(top))
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, "", fmt.Errorf("file outside its repo: %w", err)
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

// dryRunCounts — выдачи одного вида
type dryRunCounts struct {
	total, diffs, large, lost int
	maxDiff, maxLarge         int
}

func (c *dryRunCounts) add(n *Notice) {
	c.total++
	switch {
	case n.Lost:
		c.lost++
	case n.Diff != "":
		c.diffs++
		c.maxDiff = max(c.maxDiff, n.DiffBytes)
	default:
		c.large++
		c.maxLarge = max(c.maxLarge, n.DiffBytes)
	}
}

// WriteDryRun печатает итог сухого прогона
func WriteDryRun(w io.Writer, r *DryRunResult) {
	fmt.Fprintf(w, "Транскриптов с %s: %d, загрузок скиллов и инструкций: %d\n", localTime(r.Since), r.Transcripts, r.Loads)

	byKind := map[string]*dryRunCounts{kindSkill: {}, kindMemory: {}}
	byFile := map[string]int{}
	byTranscript := map[string]int{}
	total := dryRunCounts{}
	for i := range r.Notices {
		n := &r.Notices[i]
		byKind[n.Notice.Kind].add(&n.Notice)
		total.add(&n.Notice)
		byFile[n.Notice.label()]++
		byTranscript[n.Transcript]++
	}
	fmt.Fprintf(w, "Выдач: %d в %d транскриптах — diff %d (до %d Б), «перечитай» при правке больше %d Б: %d (до %d Б), текст в контексте потерян: %d\n",
		total.total, len(byTranscript), total.diffs, total.maxDiff, diffLimit, total.large, total.maxLarge, total.lost)
	for _, kind := range []string{kindSkill, kindMemory} {
		c := byKind[kind]
		fmt.Fprintf(w, "  %-7s выдач %d: diff %d (до %d Б), «перечитай» %d (до %d Б), потерян %d\n",
			kind, c.total, c.diffs, c.maxDiff, c.large, c.maxLarge, c.lost)
	}
	fmt.Fprintf(w, "Правок, оставленных Claude Code и показанных его заметкой: %d; выдач хука, задублированных заметкой Claude Code: %d\n",
		r.HandedToClaude, r.Duplicates)
	fmt.Fprintln(w, "По файлам:")
	for _, name := range sortedByCount(byFile) {
		fmt.Fprintf(w, "  %4d  %s\n", byFile[name], name)
	}
	fmt.Fprintln(w, "Больше всего в транскриптах:")
	for i, path := range sortedByCount(byTranscript) {
		if i == 10 {
			break
		}
		fmt.Fprintf(w, "  %4d  %s\n", byTranscript[path], path)
	}

	mismatch := map[string]int{}
	stale := 0
	for _, m := range r.RenderMismatch {
		label := m.Path + " (подстановка)"
		if m.Stale {
			label = m.Path + " (прежняя версия)"
			stale++
		}
		mismatch[label]++
	}
	fmt.Fprintf(w, "Загрузок, где текст в контексте не совпал с неизменённым файлом: %d из %d, из них прежняя версия файла: %d\n",
		len(r.RenderMismatch), r.Loads, stale)
	for _, label := range sortedByCount(mismatch) {
		fmt.Fprintf(w, "  %4d  %s\n", mismatch[label], label)
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
