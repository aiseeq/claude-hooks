package skillreload

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"
)

// Notice — правка файла, которую агент ещё не видел
type Notice struct {
	Kind      string // kindSkill или kindMemory
	Name      string // имя скилла или путь файла инструкций
	Path      string
	LoadedAt  time.Time // с какого момента текст у агента в контексте
	ChangedAt time.Time
	// Diff — правка от текста в контексте к файлу, если уложилась в diffLimit
	Diff string
	// DiffBytes — размер всей правки; Sections — её разделы
	DiffBytes int
	Sections  []string
	// Lost — текст, бывший у агента, не сохранился: правку не посчитать
	Lost bool
}

// compactWindow — список инструкций, приложенный не позже этого после
// сжатия, — переложенный сжатием: такие файлы Claude Code не отслеживает
const compactWindow = time.Minute

// tracker ведёт запись сессии по событиям транскрипта
type tracker struct {
	state *sessionState
	store textStore
	view  viewer
}

// apply применяет событие транскрипта к записи
func (t tracker) apply(ev transcriptEvent) error {
	switch ev.Kind {
	case evAssistant:
		t.state.LastAssistant = ev.Offset
	case evCompact:
		t.state.CompactAt = ev.At
		// После сжатия Claude Code правки загруженных файлов не показывает
		// (транскрипты 15–22.10: 0 заметок на 18 правок)
		for _, w := range t.state.Files {
			w.Native = false
			w.Deferred = ""
		}
	case evSkillLoad, evSkillReinject:
		return t.loadSkill(ev)
	case evMemoryLoad:
		return t.loadMemory(ev)
	case evNote:
		return t.note(ev)
	case evRead, evEdit:
		return t.known(ev)
	}
	return nil
}

// loadSkill запоминает снимок скилла. Сжатие подкладывает снимок прежнего
// вызова: база возвращается к нему, и правка после вызова будет показана снова
func (t tracker) loadSkill(ev transcriptEvent) error {
	snapshot := textHash(ev.Text)
	w := t.state.Files[ev.Path]
	if ev.Kind == evSkillReinject && w != nil && w.Snapshot == snapshot {
		w.Base = w.LoadBase
		return nil
	}
	view, err := t.view(kindSkill, ev.Path)
	if err != nil {
		return err
	}
	base, err := t.chooseBase(ev)
	if err != nil {
		return err
	}
	if w == nil {
		w = &watched{Kind: kindSkill, Path: ev.Path, Dir: ev.Dir}
		t.state.Files[ev.Path] = w
	}
	w.Name = skillNameFromDir(ev.Dir)
	if view.Name != "" {
		w.Name = view.Name
	}
	w.LoadedAt = ev.At
	w.Base = base
	w.Snapshot = snapshot
	w.LoadBase = base
	// Вызов скилла Claude Code за файлом следить не заставляет; прежнее
	// чтение файла инструментом — заставляет, оно остаётся в силе
	w.Deferred = ""
	return nil
}

// loadMemory запоминает файл инструкций в контексте. Список при старте и
// подгруженный по ходу файл Claude Code отслеживает до первой показанной
// правки; список, переложенный сжатием, — нет
func (t tracker) loadMemory(ev transcriptEvent) error {
	view, err := t.view(kindMemory, ev.Path)
	if err != nil {
		return err
	}
	ev.Text = renderMemory([]byte(ev.Text))
	base, err := t.chooseBase(ev)
	if err != nil {
		return err
	}
	w := t.state.Files[ev.Path]
	if w == nil {
		w = &watched{Kind: kindMemory, Path: ev.Path, Name: ev.Path}
		t.state.Files[ev.Path] = w
	}
	afterCompact := !t.state.CompactAt.IsZero() && ev.At.Sub(t.state.CompactAt).Abs() <= compactWindow
	w.LoadedAt = ev.At
	w.Base = base
	w.Native = (ev.Nested || !afterCompact) && !staleLoad(ev, view)
	w.Deferred = ""
	return nil
}

// chooseBase — текст, который у агента в контексте после загрузки: снимок из
// транскрипта, даже если файл после загрузки не менялся. Claude Code кладёт
// при старте и сжатии и прежнюю версию файла из своего кеша (транскрипты
// 15–22.10: 39 загрузок из 3153, все — версии из истории git; расхождений
// подстановки — 0): агент тогда работает по старому тексту
func (t tracker) chooseBase(ev transcriptEvent) (string, error) {
	return t.store.put(ev.Text)
}

// staleLoad — в контекст попала прежняя версия: файл изменён до загрузки,
// а текст другой. Свою заметку Claude Code сверяет со временем файла и о
// такой правке не скажет
func staleLoad(ev transcriptEvent, view fileView) bool {
	return view.Exists && view.Text != ev.Text && !view.ModTime.After(ev.At)
}

// note — заметка Claude Code об изменённом файле. Непустой фрагмент — правка
// показана: базой становится текст, который хук оставил Claude Code. Пустой
// фрагмент («изменения не показаны») — правку покажет хук. После заметки
// Claude Code файл не отслеживает (транскрипты 15–22.10: 1 заметка на 24
// следующих правки)
func (t tracker) note(ev transcriptEvent) error {
	w := t.state.Files[ev.Path]
	if w == nil {
		return nil
	}
	defer func() {
		w.Native = false
		w.Deferred = ""
	}()
	if !ev.Delivered {
		return nil
	}
	if w.Deferred != "" {
		w.Base = w.Deferred
		return nil
	}
	// Заметка пришла без отложенной правки (запись заведена позже, первое
	// чтение старой сессии): показан файл на момент заметки, если он с тех
	// пор не менялся
	view, err := t.view(w.Kind, w.Path)
	if err != nil {
		return err
	}
	if view.Exists && !view.ModTime.After(ev.At) {
		base, err := t.store.put(view.Text)
		if err != nil {
			return err
		}
		w.Base = base
	}
	return nil
}

// known — агент прочёл файл целиком или изменил его сам: текст у него в
// контексте, и Claude Code следит за файлом снова
func (t tracker) known(ev transcriptEvent) error {
	w := t.state.Files[ev.Path]
	if w == nil {
		return nil
	}
	view, err := t.view(w.Kind, w.Path)
	if err != nil || !view.Exists {
		return err
	}
	base, err := t.store.put(view.Text)
	if err != nil {
		return err
	}
	w.Base = base
	w.LoadedAt = ev.At
	w.Native = true
	w.Deferred = ""
	return nil
}

// notices сверяет файлы с текстом в контексте и собирает правки, которых
// агент не видел. Показанная правка становится базой: одна правка — одна
// выдача. Правку файла, за которым следит Claude Code, хук откладывает до
// ответа модели и показывает сам, только если заметки так и не было
func (t tracker) notices(now time.Time) ([]Notice, []error) {
	var notices []Notice
	var errs []error
	for _, path := range sortedKeys(t.state.Files) {
		w := t.state.Files[path]
		notice, ok, err := t.check(w, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s not checked: %w", w.Name, err))
		}
		if ok {
			notices = append(notices, notice)
		}
	}
	sort.SliceStable(notices, func(i, j int) bool { return notices[i].Name < notices[j].Name })
	return notices, errs
}

func (t tracker) check(w *watched, now time.Time) (Notice, bool, error) {
	view, err := t.view(w.Kind, w.Path)
	if err != nil || !view.Exists {
		return Notice{}, false, err
	}
	current := textHash(view.Text)
	if current == w.Base {
		w.Deferred = ""
		return Notice{}, false, nil
	}
	if t.announcedBySchema1(w, view) {
		base, err := t.store.put(view.Text)
		w.Base = base
		return Notice{}, false, err
	}
	if w.Native {
		if w.Deferred == "" || t.state.LastAssistant < w.DeferredAt {
			if w.Deferred == "" {
				deferred, err := t.store.put(view.Text)
				if err != nil {
					return Notice{}, false, err
				}
				w.Deferred = deferred
				w.DeferredAt = t.state.Offset
			}
			return Notice{}, false, nil
		}
		// Модель ответила, а заметки Claude Code не было
		w.Native = false
	}

	notice := Notice{Kind: w.Kind, Name: w.Name, Path: w.Path, LoadedAt: w.LoadedAt, ChangedAt: view.ModTime}
	old, err := t.store.get(w.Base)
	switch {
	case errors.Is(err, errTextLost):
		notice.Lost = true
	case err != nil:
		return Notice{}, false, err
	default:
		diff, sections := lineDiff(old, view.Text, notice.label())
		notice.DiffBytes = len(diff)
		notice.Sections = sections
		if len(diff) <= diffLimit {
			notice.Diff = diff
		}
	}
	base, err := t.store.put(view.Text)
	if err != nil {
		return Notice{}, false, err
	}
	w.Base = base
	w.Deferred = ""
	w.LoadedAt = now
	return notice, true, nil
}

// announcedBySchema1 — эту правку уже объявила хук схемы 1 («вызови
// заново»): второй раз её не показывать
func (t tracker) announcedBySchema1(w *watched, view fileView) bool {
	notified, ok := t.state.Announced[w.Path]
	if !ok {
		return false
	}
	delete(t.state.Announced, w.Path)
	raw, err := readSkillFile(filepath.Dir(w.Path))
	return err == nil && contentHash(raw.Raw) == notified && renderBody(raw.Raw, w.Dir) == view.Text
}

// label — имя файла в заголовке diff
func (n Notice) label() string {
	if n.Kind == kindSkill {
		return n.Name + "/" + skillFileName
	}
	return n.Path
}
