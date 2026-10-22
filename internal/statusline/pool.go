package statusline

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/aiseeq/claude-hooks/internal/accounts"
)

// Темп расхода пула — сумма шагов доли всех учёток из журнала
// limits-history.jsonl за окно, в процентах недели одной учётки в час
const (
	// poolRateWindow — окно темпа: трое суток. Сутки — одна ночь Сна и один
	// рабочий день: ночь тратит до 20% недели, и темп по суткам скачет
	// вдвое. Неделя запаздывает: режим работы меняется за дни (22.10 — три
	// учётки вместо одной). Трое суток — три дневных цикла, и смену режима
	// темп догоняет за пару дней
	poolRateWindow = 72 * time.Hour
	// poolRateMin — по истории короче темп не считается
	poolRateMin = 6 * time.Hour
	// historyTail — сколько хвоста журнала читает отрисовка: шагов за трое
	// суток — сотни строк, около 20 КБ; полный журнал растёт без конца
	historyTail = 256 << 10
)

// errShortHistory — истории шагов меньше poolRateMin
var errShortHistory = errors.New("limits history is too short for the pool pace")

// poolRate — темп расхода всех учёток, % недели учётки в час. Неделя
// узнаётся по сбросу. Шаг считается от прошлого шага своей недели; первый
// шаг недели — от нуля, если неделя началась после начала окна и журнала
// (раньше журнала доля недели не наблюдалась), иначе он только база. Окно
// начинается не раньше первого шага журнала
func poolRate(steps []limitStep, now time.Time) (float64, time.Duration, error) {
	from := now.Add(-poolRateWindow)
	sorted := append([]limitStep(nil), steps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	if len(sorted) == 0 {
		return 0, 0, errShortHistory
	}
	start := from
	if sorted[0].At.After(from) {
		start = sorted[0].At
	}
	span := now.Sub(start)
	if span < poolRateMin {
		return 0, span, errShortHistory
	}
	last := map[int64]float64{}
	spent := 0.0
	for _, st := range sorted {
		key := st.ResetsAt.Unix()
		prev, seen := last[key]
		last[key] = max(prev, st.SevenDay)
		if st.At.Before(from) || st.At.After(now) {
			continue
		}
		if !seen {
			if st.ResetsAt.Add(-weekWindow).Before(start) {
				continue
			}
			prev = 0
		}
		spent += max(0, st.SevenDay-prev)
	}
	return spent / span.Hours(), span, nil
}

// readRecentSteps читает хвост журнала шагов: первая неполная строка
// хвоста пропускается
func readRecentSteps(path string) ([]limitStep, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the limits history: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot read the limits history: %w", err)
	}
	reader := bufio.NewReader(f)
	if off := info.Size() - historyTail; off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return nil, fmt.Errorf("cannot read the limits history: %w", err)
		}
		reader.Reset(f)
		if _, err := reader.ReadBytes('\n'); err != nil {
			return nil, fmt.Errorf("cannot read the limits history: %w", err)
		}
	}
	var steps []limitStep
	scanner := bufio.NewScanner(reader)
	for line := 1; scanner.Scan(); line++ {
		var step limitStep
		if err := json.Unmarshal(scanner.Bytes(), &step); err != nil {
			return nil, fmt.Errorf("limits history %s tail line %d: %w", path, line, err)
		}
		steps = append(steps, step)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cannot read the limits history: %w", err)
	}
	return steps, nil
}

// PoolForecast — прогноз пула по снимку и журналу шагов на диске
type PoolForecast struct {
	accounts.Forecast
	Pace string `json:"pace"`
	// RateSpanHours — сколько часов истории дало темп (не больше окна)
	RateSpanHours float64 `json:"rateSpanHours"`
	HorizonHours  float64 `json:"horizonHours"`
}

// forecastPool считает прогноз пула учёток store по снимку snapshot
func forecastPool(store *accounts.Store, snapshot *limitsSnapshot, historyPath string, now time.Time) (PoolForecast, error) {
	steps, err := readRecentSteps(historyPath)
	if err != nil {
		return PoolForecast{}, err
	}
	rate, span, err := poolRate(steps, now)
	if err != nil {
		return PoolForecast{}, err
	}
	usage := accountUsage(snapshot, now)
	f := store.Forecast(usage, rate, now)
	pastTracks(f.Tracks, usage, steps, now)
	return PoolForecast{Forecast: f, Pace: f.Pace.String(), RateSpanHours: span.Hours(), HorizonHours: accounts.PoolHorizon.Hours()}, nil
}

// poolText — прогноз пула строкой сводки
func poolText(store *accounts.Store, f PoolForecast, now time.Time) string {
	head := fmt.Sprintf("Пул учёток: темп %.0f%% недели учётки в сутки (за %.0f ч); ", f.Rate*24, f.RateSpanHours)
	var text string
	switch f.Forecast.Pace {
	case accounts.PaceOver:
		text = fmt.Sprintf("пул кончится %s, до сброса %s %.0f ч", accounts.ShortTime(f.DryAt), store.ShortName(f.RefillEmail), f.RefillAt.Sub(f.DryAt).Hours())
	case accounts.PaceNear:
		text = fmt.Sprintf("до %s хватит впритык: при темпе на четверть выше пул кончится", accounts.ShortTime(now.Add(accounts.PoolHorizon)))
	default:
		text = fmt.Sprintf("до %s хватит с запасом", accounts.ShortTime(now.Add(accounts.PoolHorizon)))
	}
	if f.MixedTiers {
		text += "; тарифы учёток разные, а доли недели прогноз считает равными"
	}
	return head + text
}

// WritePoolJSON печатает прогноз пула JSON для страницы Сна
func WritePoolJSON(w io.Writer) error {
	paths, err := accounts.DefaultPaths()
	if err != nil {
		return err
	}
	store, err := accounts.Load(paths)
	if err != nil {
		return err
	}
	path, snapshot, _, err := readLimits()
	if err != nil {
		return err
	}
	f, err := forecastPool(store, snapshot, limitsHistory(path), time.Now())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	return enc.Encode(f)
}

// pastTracks дописывает в начало путей учёток шаги их нынешней недели за
// окно темпа: прошлое для графика пула. Неделя узнаётся по сбросу
func pastTracks(tracks []accounts.Track, usage map[string]accounts.Usage, steps []limitStep, now time.Time) {
	from := now.Add(-poolRateWindow)
	for i := range tracks {
		resets := usage[tracks[i].Email].Week.ResetsAt
		if resets.IsZero() {
			continue
		}
		var past []accounts.Point
		for _, st := range steps {
			if st.ResetsAt.Equal(resets) && !st.At.Before(from) && st.At.Before(now) {
				past = append(past, accounts.Point{At: st.At, Used: st.SevenDay})
			}
		}
		sort.SliceStable(past, func(a, b int) bool { return past[a].At.Before(past[b].At) })
		tracks[i].Points = append(past, tracks[i].Points...)
	}
}
