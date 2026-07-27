# claude-hooks

Хуки для Claude Code CLI: блокируют опасные операции до выполнения, форматируют код после записи и уведомляют о завершении сессии.

## Что делает

### Валидаторы (блокируют запись файла)

| Валидатор | Что находит |
|---|---|
| `runtime_exit` | аварийное завершение процесса (`os.Exit`, `log.Fatal`, `panic`) вне `cmd/` и `main.go` |
| `secrets` | секреты в коде: JWT-токены, wallet-адреса, ключи вида `sk_live_…`, `ghp_…` |

Тестовые файлы и документация исключаются автоматически. Комментарии и строковые литералы не считаются кодом: упоминание `panic()` в комментарии не блокирует запись.

Проверки намеренно ограничены задачами, где машина точнее человека и модели: поиск секретов по паттерну, страховка от необратимых команд, детерминированное форматирование. Стилевые нормы («не маскируй ошибки значениями по умолчанию») место в `CLAUDE.md` и в ревью, а не в лексическом фильтре: `test -f x || exit 1` и `retries := cfg.Retries || 3` для регулярного выражения неразличимы.

### Инструменты

| Инструмент | Событие | Что делает |
|---|---|---|
| `bash` | PreToolUse | блокирует опасные команды: `rm -rf /`, `mkfs`, `dd of=/dev/sda`, fork bomb, `--headed` |
| `formatter` | PostToolUse | запускает `gofmt` для Go и `prettier` для TS/JS |
| `notifier` | Stop, Notification | звук и desktop-уведомление: когда Claude закончил работу и когда спрашивает разрешение или ждёт ответа. Клик по уведомлению переводит фокус на окно терминала |

## Установка

```bash
git clone https://github.com/aiseeq/claude-hooks.git
cd claude-hooks
make install
```

Устанавливает бинарь в `~/.claude/hooks/claude-hooks` и конфигурацию в `~/.claude/hooks/config.yaml`.
Существующая конфигурация не перезаписывается: новая версия кладётся рядом как `config.yaml.new`.

Затем добавь блок `hooks` из [`configs/settings-snippet.json`](configs/settings-snippet.json) в `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit",
        "hooks": [{ "type": "command", "command": "$HOME/.claude/hooks/claude-hooks pre-tool-use", "timeout": 5 }]
      },
      {
        "matcher": "Bash",
        "hooks": [{ "type": "command", "command": "$HOME/.claude/hooks/claude-hooks pre-tool-use", "timeout": 3 }]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Write|Edit|MultiEdit",
        "hooks": [{ "type": "command", "command": "$HOME/.claude/hooks/claude-hooks post-tool-use", "timeout": 5 }]
      }
    ],
    "Notification": [
      { "hooks": [{ "type": "command", "command": "$HOME/.claude/hooks/claude-hooks notification", "timeout": 3 }] }
    ],
    "Stop": [
      { "hooks": [{ "type": "command", "command": "$HOME/.claude/hooks/claude-hooks stop", "timeout": 3 }] }
    ]
  }
}
```

`timeout` в настройках Claude Code задаётся **в секундах**.

## Конфигурация

`~/.claude/hooks/config.yaml` — см. комментарии в [`configs/hooks.yaml`](configs/hooks.yaml).

Исключения (`exceptions`) поддерживают три формы записи:

```yaml
exceptions:
  - "*_test.go"            # glob по имени файла
  - "src/*/generated.go"   # glob по пути
  - "/vendor/"             # подстрока пути
```

Неизвестные ключи в конфигурации считаются ошибкой — опечатка в имени поля не отключит проверку молча.

Проверить и посмотреть текущую конфигурацию:

```bash
claude-hooks config validate
claude-hooks config show
```

### Имя проекта в уведомлении

Берётся из рабочего каталога сессии, без привязки к какому-либо одному месту для проектов:

| Каталог | Имя в уведомлении |
|---|---|
| `~/work/claude-hooks` | `claude-hooks` |
| `~/git/life` | `life` |
| `~` | `~` |
| `~/work/saga/backend` | `saga/backend` |

Вложенные проекты показываются вместе с родительским каталогом: иначе `~/work/saga/backend` и `~/work/glint/backend` выглядели бы одинаково.

Если Claude Code не передал рабочий каталог, имя восстанавливается из пути транскрипта (`~/.claude/projects/-home-user-work-claude-hooks/`). Кодирование там неоднозначно — дефис бывает и разделителем каталогов, и частью имени, — поэтому вариант разбора проверяется по файловой системе.

## Переход к окну по клику

Когда Claude ждёт ответа, клик по уведомлению переключает фокус на то окно терминала, из которого пришёл вопрос — даже если окон Konsole открыто восемь.

Как это устроено:

1. Хук поднимается по цепочке `/proc/<pid>/stat` от себя до корня и собирает PID всех процессов-предков. Окно принадлежит одному из них, и знать конкретный эмулятор терминала не требуется.
2. Уведомление отправляется напрямую через `org.freedesktop.Notifications` с действием `default` — в KDE оно срабатывает при клике по телу уведомления, а не только по кнопке.
3. Ожидание клика вынесено в отдельный процесс (`setsid`): хук завершается за считанные миллисекунды, Claude Code его не ждёт.
4. По клику в KWin загружается временный JS-скрипт, который находит окно с подходящим PID и присваивает `workspace.activeWindow`.

Четвёртый шаг — единственный способ передать фокус в Wayland: X11-API удалены, а протокол `xdg-activation` требует токена, выданного в ответ на действие пользователя. Изнутри компоновщика это ограничение не действует, поэтому код выполняется в самом KWin. На X11 работает тот же путь.

Требуется KDE Plasma (KWin 6). В других окружениях уведомление показывается, но без действия по клику — сбоя не происходит. Отключается опцией `activate_on_click: false`.

## Как это работает

Claude Code передаёт хуку JSON на stdin и интерпретирует код возврата:

| Код | Значение |
|---|---|
| 0 | операция разрешена |
| 2 | операция заблокирована, stderr передаётся модели |
| 1 | ошибка самого хука — операция не блокируется |

Логи пишутся в `~/.claude/logs/claude-hooks.log` и ротируются при превышении `max_size_mb`.

## Разработка

```bash
make build      # сборка
make test       # тесты
make test-race  # тесты с детектором гонок
make cover      # покрытие
make lint       # go vet + golangci-lint
make fmt        # форматирование
```

## Требования

- Linux, Go 1.21+
- `gofmt` (входит в Go), `prettier` — опционально, для TS/JS
- `canberra-gtk-play` или `paplay` — опционально, для звука уведомлений
- KDE Plasma (KWin 6) — для перехода к окну по клику

## Лицензия

MIT
