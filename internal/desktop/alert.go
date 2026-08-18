package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// maxWatchLifetime ограничивает время жизни фонового наблюдателя:
// уведомление к этому моменту давно закрыто, а процесс висеть не должен
const maxWatchLifetime = 5 * time.Minute

// soundCandidate способ воспроизведения звука в порядке приоритета
type soundCandidate struct {
	command string
	args    []string
	// file — звуковой файл, который должен существовать (пусто, если не требуется)
	file string
}

var soundCandidates = []soundCandidate{
	{command: "canberra-gtk-play", args: []string{"-i", "window-attention"}},
	{command: "paplay", file: "/usr/share/sounds/freedesktop/stereo/window-attention.oga"},
	{command: "paplay", file: "/usr/share/sounds/alsa/Front_Left.wav"},
}

// Alert описывает оповещение пользователя: звук, уведомление и окно,
// на которое нужно перевести фокус по клику
type Alert struct {
	Title   string
	Message string
	AppName string
	Icon    string
	Timeout time.Duration

	Sound   bool
	Desktop bool

	// ActivatePIDs — процессы, чьё окно активируется по клику.
	// Пустой список отключает действие у уведомления
	ActivatePIDs []int
	ActionLabel  string
}

// Deliver показывает оповещение и, если пользователь нажал на уведомление,
// переводит фокус на его окно. Метод блокирующий: вызывать из фонового процесса.
// Звук и уведомление независимы: сбой одного не отменяет другого, ошибки
// обоих возвращаются вместе
func Deliver(ctx context.Context, alert Alert) error {
	var soundErr error
	if alert.Sound {
		if err := playSound(); err != nil {
			soundErr = fmt.Errorf("sound: %w", err)
		}
	}

	if !alert.Desktop {
		return soundErr
	}

	return errors.Join(soundErr, deliverDesktop(ctx, alert))
}

// deliverDesktop показывает уведомление и ждёт клика по нему
func deliverDesktop(ctx context.Context, alert Alert) error {
	watchCtx, cancel := context.WithTimeout(ctx, maxWatchLifetime)
	defer cancel()

	conn, err := dbus.SessionBus()
	if err != nil {
		return fmt.Errorf("session bus unavailable: %w", err)
	}

	canActivate := len(alert.ActivatePIDs) > 0 && WindowActivationAvailable(conn)

	notification := Notification{
		AppName: alert.AppName,
		Icon:    alert.Icon,
		Title:   alert.Title,
		Message: alert.Message,
		Timeout: alert.Timeout,
	}
	if canActivate {
		notification.ActionLabel = alert.ActionLabel
	}

	id, err := Send(conn, notification)
	if err != nil {
		return err
	}

	if !canActivate {
		return nil
	}

	clicked, err := WaitForAction(watchCtx, conn, id)
	if err != nil {
		return err
	}
	if !clicked {
		return nil
	}

	return ActivateWindowByPIDs(conn, alert.ActivatePIDs)
}

// DeliverInBackground запускает доставку оповещения в отдельном процессе и сразу
// возвращает управление: хук не должен ждать ни звука, ни тем более клика.
// executable — путь к собственному бинарю, watchCommand — имя его скрытой подкоманды
func DeliverInBackground(executable, watchCommand string, alert Alert) error {
	args := []string{
		watchCommand,
		"--title", alert.Title,
		"--message", alert.Message,
		"--app-name", alert.AppName,
		"--icon", alert.Icon,
		"--timeout", alert.Timeout.String(),
		"--sound=" + strconv.FormatBool(alert.Sound),
		"--desktop=" + strconv.FormatBool(alert.Desktop),
	}
	if len(alert.ActivatePIDs) > 0 {
		args = append(args, "--pids", joinInts(alert.ActivatePIDs), "--action-label", alert.ActionLabel)
	}

	cmd := exec.Command(executable, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	detachProcess(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start notification watcher: %w", err)
	}

	// Процесс переживёт хук: ждать его завершения нельзя, но и зомби оставлять не нужно
	go cmd.Wait()

	return nil
}

// playSound проигрывает звук первым доступным способом и не ждёт его окончания.
// Если не подошёл ни один, ошибка перечисляет, чем не подошёл каждый
func playSound() error {
	var errs []error
	for _, candidate := range soundCandidates {
		if err := candidate.play(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", candidate.command, err))
			continue
		}
		return nil
	}
	return fmt.Errorf("no sound backend worked: %w", errors.Join(errs...))
}

// play запускает воспроизведение, если плеер и звуковой файл на месте
func (c soundCandidate) play() error {
	if c.file != "" {
		if _, err := os.Stat(c.file); err != nil {
			return err
		}
	}
	if _, err := exec.LookPath(c.command); err != nil {
		return err
	}

	args := c.args
	if c.file != "" {
		args = append(append([]string{}, args...), c.file)
	}
	return exec.Command(c.command, args...).Start()
}

// joinInts преобразует список чисел в строку через запятую
func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

// ParseInts разбирает список чисел, записанных через запятую
func ParseInts(raw string) ([]int, error) {
	if raw == "" {
		return nil, nil
	}

	var values []int
	for _, part := range strings.Split(raw, ",") {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("invalid pid list %q: %w", raw, err)
		}
		values = append(values, value)
	}
	return values, nil
}
