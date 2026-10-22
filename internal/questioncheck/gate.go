package questioncheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Scope — чьи концы хода проверка берёт на себя
type Scope struct {
	ClaudeDir string
	Targets   []Target
}

// GateStatus — что проверка сделает с концом хода
type GateStatus int

const (
	// GateSkip — не её конец хода: срок, субагент, чужая сессия, нет вопроса
	GateSkip GateStatus = iota
	// GateAfterBlock — ход после остановки: только запись в журнал
	GateAfterBlock
	// GateJudge — проверка позовёт судью
	GateJudge
)

// Gate — решение, звать ли судью, и из чего оно сделано
type Gate struct {
	Status    GateStatus
	Skip      string
	Target    Target
	Questions []Question
}

// Gate решает, позовёт ли проверка судью. Им же пользуется Stop-хук
// оповещений: на GateJudge он не звонит, звонит question-check после
// вердикта. Транскрипт не читается — только вход хука и запись сессии
func (s Scope) Gate(now time.Time, event Event) Gate {
	if !now.Before(Until) {
		return Gate{Skip: "эксперимент окончен " + Until.Format("02.01 15:04 MST")}
	}
	if event.AgentID != "" {
		return Gate{Skip: "субагент"}
	}
	name, err := SessionName(s.ClaudeDir, event.ClaudePID)
	if err != nil {
		return Gate{Skip: "имя сессии не прочитано: " + err.Error()}
	}
	target, ok := MatchTarget(s.Targets, name, event.TranscriptPath)
	if !ok {
		return Gate{Skip: "не своя сессия: " + name}
	}

	gate := Gate{Target: target, Questions: Questions(event.LastMessage)}
	switch {
	case event.StopHookActive:
		gate.Status = GateAfterBlock
		gate.Skip = "stop_hook_active"
	case len(gate.Questions) == 0:
		gate.Skip = "вопроса нет"
	default:
		gate.Status = GateJudge
	}
	return gate
}

// HookCommand — подкоманда, по которой проверка видна в settings.json
const HookCommand = "claude-hooks question-check"

// Registered — стоит ли проверка в Stop-хуках settings.json. Stop-хук
// оповещений отдаёт ей звонок только тогда: установленный бинарник без
// записи в settings.json иначе заглушил бы конец хода, и не позвонил бы никто
func Registered(settingsPath string) (bool, error) {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read settings: %w", err)
	}
	var settings struct {
		Hooks struct {
			Stop []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"Stop"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, fmt.Errorf("parse settings %s: %w", settingsPath, err)
	}
	for _, group := range settings.Hooks.Stop {
		for _, hook := range group.Hooks {
			if strings.Contains(hook.Command, HookCommand) {
				return true, nil
			}
		}
	}
	return false, nil
}
