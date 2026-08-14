Контекст
- claude-hooks (github.com/aiseeq/claude-hooks) — Go-хуки для Claude Code: блокировка опасных команд, форматирование, уведомления, строка статуса. Единственный remote — `origin` (github), trunk — `main`.
- `make install` собирает и ставит бинарь в `~/.claude/hooks`. После изменения хуков обязательно переустановить, иначе все сессии продолжают работать со старой версией.
- Версия берётся из `git describe` — файла VERSION и bump-скриптов в проекте нет.

Коммиты
- `make commit MESSAGE="..."` — единственный способ: smoke (сборка + тесты) + stage all + commit + push; отдельный `git push` после него не запускать. Разрешён без запроса, так часто, как нужно.
- Завершённая работа заканчивается `make commit` сразу; незакоммиченное рабочее дерево не оставлять, при массовых правках — промежуточные коммиты. MESSAGE — Conventional Commits.
- Без запроса разрешены: read-only команды, `git fetch`, `git switch <existing-branch>`, `git switch -c <branch> origin/main`, `git merge --ff-only`, `git pull --ff-only`.
- Требуют разрешения: `add`, raw `commit`, `reset`, `restore`, `checkout --`, `clean`, `stash`, `rebase`, `cherry-pick`, обычный merge, удаление веток, force-push, теги.
- Перед коммитом смотреть только на секреты и большие бинарники.
