package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/aiseeq/claude-hooks/internal/accounts"
	"github.com/aiseeq/claude-hooks/internal/core"
	"github.com/aiseeq/claude-hooks/internal/statusline"
)

// rotateTimeout — потолок фоновой ротации: сетевые запросы и ожидание
// блокировок Claude Code
const rotateTimeout = 3 * time.Minute

// newAccountCmd создаёт команды учёток подписок
func newAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Учётки подписок Claude: список, сохранение, переключение",
		Long: `Хранит токены нескольких подписок Claude и переключает между ними
Claude Code без нового входа. Хранилище — ~/.local/state/claude-hooks/accounts.json
(права 600, токены не печатаются).

Вход в новую учётку — /login в любой сессии Claude Code: строка статуса
сама сохранит его. /logout не использовать: он отзывает токены учётки.

Автоматическое переключение: из учёток с остатком недели (меньше 98%,
пять часов меньше 95%) выбирается та, чья неделя сбросится раньше, —
её остаток не сгорит. Запущенные сессии берут новую учётку на следующем
запросе к API.

Без подкоманды печатает сводку учёток, как claude-hooks limits.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return statusline.WriteLimits(cmd.OutOrStdout(), false)
		},
	}
	cmd.AddCommand(newAccountSaveCmd(), newAccountSwitchCmd(), newAccountAutoCmd(), newAccountForgetCmd(), newAccountRotateCmd())
	return cmd
}

// newAccountSaveCmd — сохранить текущий вход
func newAccountSaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save",
		Short: "Сохранить текущий вход Claude Code в хранилище",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRotator()
			if err != nil {
				return err
			}
			if _, err := r.Rotate(cmd.Context(), accounts.Options{Force: true, Wait: true}); err != nil {
				return err
			}
			s, err := accounts.Load(r.Paths)
			if err != nil {
				return err
			}
			if email := s.ActiveEmail(); email != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "сохранена %s\n", email)
				return nil
			}
			return fmt.Errorf("current Claude Code login not recognised")
		},
	}
}

// newAccountSwitchCmd — ручное переключение
func newAccountSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <почта>",
		Short: "Переключить Claude Code на учётку и выключить автопереключение",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRotator()
			if err != nil {
				return err
			}
			email, err := r.Switch(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "активна %s; автопереключение выключено (claude-hooks account auto on)\n", email)
			return nil
		},
	}
}

// newAccountAutoCmd — включить и выключить автопереключение
func newAccountAutoCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "auto on|off",
		Short:     "Включить или выключить автоматическое переключение",
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := accounts.DefaultPaths()
			if err != nil {
				return err
			}
			on := args[0] == "on"
			if err := accounts.SetAuto(paths, on); err != nil {
				return err
			}
			state := "выключено"
			if on {
				state = "включено"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "автопереключение %s\n", state)
			return nil
		},
	}
}

// newAccountForgetCmd — удалить учётку
func newAccountForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <почта>",
		Short: "Удалить учётку из хранилища",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := accounts.DefaultPaths()
			if err != nil {
				return err
			}
			email, err := accounts.Forget(paths, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "удалена %s\n", email)
			return nil
		},
	}
}

// newAccountRotateCmd — фоновая ротация
func newAccountRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "Сверить хранилище и переключить по правилу (запускает строка статуса)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newRotator()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), rotateTimeout)
			defer cancel()
			_, err = r.Rotate(ctx, accounts.Options{})
			return err
		},
	}
}

// newRotator собирает ротацию с логгером из конфигурации хуков
func newRotator() (*accounts.Rotator, error) {
	config, err := core.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	logger, err := core.NewLogger(config.Logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create logger: %w", err)
	}
	paths, err := accounts.DefaultPaths()
	if err != nil {
		return nil, err
	}
	return &accounts.Rotator{
		Paths:  paths,
		Client: accounts.NewClient("claude-hooks/" + Version),
		Limits: statusline.SnapshotLimits{},
		Log:    logger,
		Now:    time.Now,
	}, nil
}
