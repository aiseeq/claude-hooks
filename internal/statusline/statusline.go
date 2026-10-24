package statusline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aiseeq/claude-hooks/internal/accounts"
	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/desktop"
)

// ANSI-последовательности. Строка статуса должна читаться боковым зрением,
// поэтому имя проекта выводится плашкой, а не обычным текстом.
//
// Цвета заданы палитрой из 256 значений, а не восьмицветной: базовые цвета
// терминала темы переопределяют, и жёлтый фон выходит грязно-коричневым
const (
	reset  = "\033[0m"
	dim    = "\033[2;37m"
	green  = "\033[38;5;77m"
	yellow = "\033[38;5;214m"
	red    = "\033[38;5;203m"
	cyan   = "\033[38;5;81m"

	badgeBlue  = "\033[1;38;5;231;48;5;33m"
	badgeAmber = "\033[1;38;5;16;48;5;214m"
	badgeRed   = "\033[1;38;5;231;48;5;196m"
)

// Пороги заполнения контекста, при которых меняется цвет полосы
const (
	contextWarnPercent     = 60
	contextCriticalPercent = 85
)

// barSegments — клеток в полосе доли; клетка — 10%
const barSegments = 10

// weekWindow — недельное окно лимита: 168 часов до resets_at
const weekWindow = 168 * time.Hour

// weekNearPercent — прогноз на сброс, выше которого неделя желтеет: лимита
// хватит, но запаса почти нет
const weekNearPercent = 80

// Input описывает данные, которые Claude Code передаёт строке статуса
type Input struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`

	Model struct {
		DisplayName string `json:"display_name"`
	} `json:"model"`

	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`

	ContextWindow struct {
		UsedPercentage float64 `json:"used_percentage"`
	} `json:"context_window"`

	Cost struct {
		LinesAdded   int `json:"total_lines_added"`
		LinesRemoved int `json:"total_lines_removed"`
	} `json:"cost"`

	Effort struct {
		Level string `json:"level"`
	} `json:"effort"`

	RateLimits struct {
		SevenDay *WeeklyLimit `json:"seven_day"`
		FiveHour *WeeklyLimit `json:"five_hour"`
	} `json:"rate_limits"`
}

// Render читает данные Claude Code и возвращает строку статуса.
// Попутно обновляется заголовок окна: строка статуса видна только в активном
// окне, а по заголовку сессию видно в панели задач и в переключателе окон.
func Render(stdin io.Reader, logger core.Logger) (string, error) {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("failed to read status line input: %w", err)
	}

	var input Input
	if err := json.Unmarshal(data, &input); err != nil {
		return "", fmt.Errorf("failed to parse status line input: %w", err)
	}

	now := time.Now()
	store, paths := loadAccounts(logger)
	owner := ""
	var snapshot *limitsSnapshot
	limit := input.RateLimits.SevenDay
	if week, ok := limit.usage(); ok {
		smp := sample{week: &week, session: input.SessionID, account: store.ActiveEmail()}
		if five, ok := input.RateLimits.FiveHour.usage(); ok {
			smp.five = &five
		}
		res, err := recordLimits(smp, now)
		switch {
		case err != nil:
			// Явный failover: снимок не сведён, полоса рисуется по доле самой сессии
			logger.Warn("weekly limit not saved, showing the session one", "error", err)
		case res.verdict == weekStale:
			// Полоса показывает свежайшую долю своей недели: своя доля
			// сессии — из её последнего ответа API, он мог быть часы назад
			logger.Info("stale weekly limit, showing the recorded one", "session", input.SessionID,
				"session_used", week.used, "recorded_used", res.merged.used, "resets_at", week.resetsAt.Format(time.RFC3339))
			input.RateLimits.SevenDay = res.merged.limit()
		}
		owner, snapshot = res.owner, res.snapshot
	} else if limit != nil {
		logger.Warn("weekly limit incomplete or invalid, not shown",
			"has_used_percentage", limit.UsedPercentage != nil, "has_resets_at", limit.ResetsAt != nil)
	}
	if active := store.ActiveEmail(); active != "" && owner != active {
		// Доля во входе — из последнего ответа API сессии: после переключения
		// он от прежней учётки, а после /login его нет вовсе, пока сессия не
		// сделает запрос. Полоса показывает неделю учётки, что сейчас у Claude
		// Code, если она известна
		if snapshot == nil {
			var err error
			if snapshot, err = diskLimits(); err != nil {
				// Сбой не срывает строку статуса: полосы нет, сбой — в лог
				logger.Warn("limits snapshot unreadable, weekly bar of the active account not shown", "error", err)
			}
		}
		if week, ok := snapshot.weekOf(active, now); ok {
			input.RateLimits.SevenDay = week.limit()
			owner = active
		}
	}
	if store != nil {
		rotate(paths, store, snapshot, now, logger)
	}
	bar := weekBar{label: "нед"}
	if store != nil && len(store.Accounts) > 1 && owner != "" {
		// Учёток несколько: полоса подписана учёткой, чья неделя, без «нед»
		// (пользователь 22.10: достаточно «<учётка> ▓▓▓▓▓▓▓▓▓░ 96%»)
		bar.label = store.ShortName(owner)
	}
	if store != nil && len(store.Accounts) > 0 {
		bar.pool = poolPace(store, snapshot, now, logger)
	}

	line, title := build(input, bar, now, logger)
	if err := desktop.SetTerminalTitle(title); err != nil {
		logger.Warn("terminal title not set", "error", err)
	}

	return line, nil
}

// build собирает строку статуса и заголовок окна. Сбой чтения состояния
// сессии строку не срывает: она рисуется по тому, что удалось прочитать, а
// сбой уходит в лог. Ветки и worktree в строке нет (пользователь 24.10 о
// строке всех агентов: «Хочу видеть только вот это»)
func build(input Input, bar weekBar, now time.Time, logger core.Logger) (string, string) {
	dir := sessionDir(input)
	state, err := core.LoadSessionState(input.SessionID)
	if err != nil {
		// Явный failover: без записи состояние помечается неизвестным, и
		// плашка остаётся нейтральной — ни «ждёт ответа», ни «готово»
		logger.Warn("session state unavailable", "session", input.SessionID, "error", err)
		state = core.StateUnknown
	}

	// Путь не показывается: плашка уже называет проект, а полный путь
	// повторяет её и занимает место
	var output strings.Builder
	output.WriteString(projectBadge(dir, state, input.ContextWindow.UsedPercentage))

	for _, part := range details(input, bar, now) {
		output.WriteString(dim + " · " + reset + part)
	}

	return output.String(), terminalTitle(dir, state, input.ContextWindow.UsedPercentage)
}

// terminalTitle собирает заголовок окна: значок состояния и проект.
// Значок идёт первым — по нему окно находится взглядом в панели задач; ветка
// есть в строке статуса, во вкладке она вытесняет имя проекта
func terminalTitle(dir string, state core.SessionState, contextUsed float64) string {
	marker := "🔵"
	switch {
	case contextUsed >= contextCriticalPercent:
		marker = "🔴"
	case state == core.StateWaiting:
		marker = "🟡"
	case state == core.StateDone:
		marker = "✅"
	}

	return marker + " " + core.ProjectNameForDir(dir)
}

// projectBadge рисует имя проекта плашкой, цвет которой показывает состояние.
// Переполненный контекст важнее прочего: скоро сожмётся история
func projectBadge(dir string, state core.SessionState, contextUsed float64) string {
	badge := badgeBlue
	switch {
	case contextUsed >= contextCriticalPercent:
		badge = badgeRed
	case state == core.StateWaiting:
		badge = badgeAmber
	}

	name := strings.ToUpper(core.ProjectNameForDir(dir))
	return badge + " " + name + " " + reset
}

// details собирает вторую строку из доступных сведений
func details(input Input, bar weekBar, now time.Time) []string {
	var parts []string

	if model := shortModel(input.Model.DisplayName); model != "" {
		if effort := input.Effort.Level; effort != "" {
			model += "·" + effort
		}
		parts = append(parts, cyan+model+reset)
	}

	if used := input.ContextWindow.UsedPercentage; used > 0 {
		parts = append(parts, contextBar(used))
	}

	if week, ok := input.RateLimits.SevenDay.usage(); ok {
		parts = append(parts, weeklyBar(bar, week, now))
	}

	if added, removed := input.Cost.LinesAdded, input.Cost.LinesRemoved; added > 0 || removed > 0 {
		parts = append(parts, fmt.Sprintf("%s+%d%s %s−%d%s", green, added, reset, red, removed, reset))
	}

	return parts
}

// shortModel убирает уточнение в скобках: «Opus 5 (1M context)» занимает
// в одной строке заметное место, а размер контекста и так виден по полосе
func shortModel(model string) string {
	if open := strings.Index(model, " ("); open > 0 {
		model = model[:open]
	}
	return strings.TrimSpace(model)
}

// contextBar рисует полосу заполнения контекстного окна
func contextBar(used float64) string {
	color := green
	switch {
	case used >= contextCriticalPercent:
		color = red
	case used >= contextWarnPercent:
		color = yellow
	}
	return meter("ctx", used, color, "")
}

// weekBar — подпись полосы недели и прогноз пула учёток; pool nil —
// прогноза пула нет, цвет — по прогнозу одной недели
type weekBar struct {
	label string
	pool  *pace
}

// weeklyBar рисует долю недельного лимита активной учётки. Цвет — прогноз:
// пула учёток, если он есть, иначе недели на сброс. Значка нет —
// пользователю цвета достаточно (22.10: «мне цвета достаточно в целом»)
func weeklyBar(bar weekBar, week weeklyUsage, now time.Time) string {
	p := weeklyPace(week, now)
	if bar.pool != nil {
		p = *bar.pool
	}
	switch p {
	case paceOver:
		return meter(bar.label, week.used, red, "")
	case paceNear:
		return meter(bar.label, week.used, yellow, "")
	default:
		return meter(bar.label, week.used, green, "")
	}
}

// poolPace — прогноз пула учёток для цвета полосы. Сбой (нет снимка,
// короткая история) — явный failover на прогноз одной недели, сбой — в лог
func poolPace(store *accounts.Store, snapshot *limitsSnapshot, now time.Time, logger core.Logger) *pace {
	path, err := LimitsFile()
	if err == nil && snapshot == nil {
		snapshot, err = diskLimits()
	}
	var f PoolForecast
	if err == nil {
		f, err = forecastPool(store, snapshot, limitsHistory(path), now)
	}
	if err != nil {
		logger.Info("pool forecast unavailable, weekly bar colored by the active week", "error", err)
		return nil
	}
	var p pace
	switch f.Forecast.Pace {
	case accounts.PaceSafe:
		p = paceSafe
	case accounts.PaceNear:
		p = paceNear
	case accounts.PaceOver:
		p = paceOver
	}
	return &p
}

// meter рисует долю полосой: подпись, клетки, проценты и пометка. Одна форма
// для контекста и недели, цвет выбирает вызывающий
func meter(label string, used float64, color, mark string) string {
	if used > 100 {
		used = 100
	}

	filled := int(used) * barSegments / 100
	bar := strings.Repeat("▓", filled) + strings.Repeat("░", barSegments-filled)
	return fmt.Sprintf("%s%s %s%s %.0f%%%s%s", dim, label, color, bar, used, mark, reset)
}

// pace — прогноз недельного лимита
type pace int

const (
	// paceSafe — прогноз на сброс до 80%: хватит с запасом
	paceSafe pace = iota
	// paceNear — прогноз больше 80% и до 100%: хватит впритык
	paceNear
	// paceOver — прогноз больше 100%: лимит кончится до сброса
	paceOver
)

// weeklyPace прогнозирует неделю по среднему темпу с начала окна.
// Окно — 168 часов до resets_at; now передаёт вызывающий, чтобы расчёт
// не зависел от часов машины
func weeklyPace(week weeklyUsage, now time.Time) pace {
	elapsed := now.Sub(week.resetsAt.Add(-weekWindow)).Hours()
	if elapsed <= 0 {
		// Окно только началось: темп за нулевое время не определён. Ничего
		// не потрачено — хватит; уже потрачено — темп без предела, кончится
		if week.used > 0 {
			return paceOver
		}
		return paceSafe
	}

	// Сброс уже прошёл, а замер старый: часов до сброса не осталось
	remaining := max(week.resetsAt.Sub(now).Hours(), 0)
	rate := week.used / elapsed
	return classifyPace(week.used + rate*remaining)
}

// classifyPace относит прогноз на сброс (%) к цвету
func classifyPace(projected float64) pace {
	switch {
	case projected > 100:
		return paceOver
	case projected > weekNearPercent:
		return paceNear
	default:
		return paceSafe
	}
}

// sessionDir — каталог, где запущена сессия: им называется проект в плашке и
// заголовке. Текущий каталог агента уходит в подпапки и чужие проекты (cd по
// ходу работы), и заголовок вкладки тогда называл бы не ту сессию
func sessionDir(input Input) string {
	return firstDir(input.Workspace.ProjectDir, input.CWD, input.Workspace.CurrentDir)
}

func firstDir(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate
		}
	}

	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// loadAccounts читает хранилище учёток. Сбой не срывает строку статуса:
// неделя пишется без учётки, ротации нет
func loadAccounts(logger core.Logger) (*accounts.Store, accounts.Paths) {
	paths, err := accounts.DefaultPaths()
	if err != nil {
		logger.Warn("accounts paths unavailable, no rotation", "error", err)
		return nil, paths
	}
	store, err := accounts.Load(paths)
	if err != nil {
		logger.Warn("accounts store unreadable, no rotation", "error", err)
		return nil, paths
	}
	return store, paths
}

// rotateArgs — подкоманда фоновой ротации учёток
var rotateArgs = []string{"account", "rotate"}

// startRotation запускает фоновую ротацию; тесты подменяют его, чтобы не
// запускать свой бинарь
var startRotation = accounts.MaybeRotate

// rotate запускает фоновую ротацию учёток, если ей есть работа. Снимок из
// отрисовки, а без замера недели — с диска
func rotate(paths accounts.Paths, store *accounts.Store, snapshot *limitsSnapshot, now time.Time, logger core.Logger) {
	var usage map[string]accounts.Usage
	if snapshot != nil {
		usage = accountUsage(snapshot, now)
	} else {
		var err error
		if usage, err = (SnapshotLimits{}).Usage(); err != nil {
			logger.Warn("limits snapshot unreadable, no rotation", "error", err)
			return
		}
	}
	executable, err := os.Executable()
	if err != nil {
		logger.Warn("own executable unknown, no rotation", "error", err)
		return
	}
	why, err := startRotation(paths, store, usage, now, executable, rotateArgs...)
	if err != nil {
		logger.Warn("account rotation not started", "error", err)
		return
	}
	if why != "" {
		logger.Info("account rotation started", "reason", why)
	}
}
