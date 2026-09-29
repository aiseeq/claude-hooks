package statusline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// gitStatus читает статус репозитория и проваливает тест на ошибке git
func gitStatus(t *testing.T, ctx context.Context, dir string) GitStatus {
	t.Helper()
	status, err := ReadGitStatus(ctx, dir)
	if err != nil {
		t.Fatalf("ReadGitStatus(%s): %v", dir, err)
	}
	return status
}

func TestReadGitStatusOutsideRepo(t *testing.T) {
	// Каталог без репозитория: строка статуса просто не показывает ветку
	status := gitStatus(t, context.Background(), t.TempDir())
	if status.IsRepo {
		t.Errorf("каталог без репозитория помечен как репозиторий: %+v", status)
	}
}

func TestReadGitStatusReportsBranchAndChanges(t *testing.T) {
	repo := initRepo(t)

	status := gitStatus(t, context.Background(), repo)
	if !status.IsRepo {
		t.Fatal("репозиторий не распознан")
	}
	if status.Branch == "" || status.Detached {
		t.Errorf("ожидалась именованная ветка, получено %+v", status)
	}
	if !status.Counted || status.Changed != 0 {
		t.Errorf("свежий коммит должен давать чистое дерево, получено %+v", status)
	}

	writeFile(t, filepath.Join(repo, "file.txt"), "изменение")

	status = gitStatus(t, context.Background(), repo)
	if status.Changed != 1 || !status.Counted {
		t.Errorf("изменение файла не учтено: %+v", status)
	}
}

func TestReadGitStatusIgnoresUntrackedFiles(t *testing.T) {
	// Обход неотслеживаемых файлов в больших деревьях заметно дороже,
	// а строка статуса рисуется на каждое сообщение
	repo := initRepo(t)
	writeFile(t, filepath.Join(repo, "новый.txt"), "содержимое")

	if status := gitStatus(t, context.Background(), repo); status.Changed != 0 {
		t.Errorf("неотслеживаемый файл попал в счётчик изменений: %+v", status)
	}
}

func TestReadGitStatusCountsUpstreamDivergence(t *testing.T) {
	upstream := initRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, "", "clone", "--quiet", upstream, clone)

	writeFile(t, filepath.Join(clone, "file.txt"), "локальная правка")
	runGit(t, clone, "commit", "--quiet", "-am", "локальный коммит")
	writeFile(t, filepath.Join(upstream, "file.txt"), "правка в upstream")
	runGit(t, upstream, "commit", "--quiet", "-am", "коммит в upstream")
	runGit(t, clone, "fetch", "--quiet")

	status := gitStatus(t, context.Background(), clone)
	if status.Ahead != 1 || status.Behind != 1 || !status.Counted {
		t.Errorf("ожидалось ↑1 ↓1, получено %+v", status)
	}
}

func TestParseStatusV2(t *testing.T) {
	output := strings.Join([]string{
		"# branch.oid 2462832b77522ec8ab336bc75aa9d219f981b288",
		"# branch.head main",
		"# branch.upstream origin/main",
		"# branch.ab +3 -2",
		"1 .M N... 100644 100644 100644 aaa bbb internal/a.go",
		"2 R. N... 100644 100644 100644 aaa bbb R100 new.go\told.go",
	}, "\n")

	changed, ahead, behind, err := parseStatusV2(output)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if changed != 2 || ahead != 3 || behind != 2 {
		t.Errorf("получено changed=%d ahead=%d behind=%d", changed, ahead, behind)
	}

	if _, _, _, err := parseStatusV2("# branch.ab +x -2"); err == nil {
		t.Error("испорченная строка branch.ab должна давать ошибку")
	}
}

// Одно обновление индекса на репозиторий: второе при занятой блокировке выходит сразу
func TestRefreshIndex(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	repo := initRepo(t)

	refreshed, err := RefreshIndex(context.Background(), repo)
	if err != nil || !refreshed {
		t.Fatalf("обновление индекса: refreshed=%v err=%v", refreshed, err)
	}

	lockPath, err := refreshLockPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	refreshed, err = RefreshIndex(context.Background(), repo)
	if err != nil || refreshed {
		t.Errorf("при занятой блокировке обновление не запускается: refreshed=%v err=%v", refreshed, err)
	}
}

// runGit выполняет git без пользовательских настроек, прерывая тест при ошибке
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

// initRepo создаёт репозиторий с одним коммитом
func initRepo(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не установлен")
	}

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "file.txt"), "начало")

	commands := [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "test"},
		{"add", "file.txt"},
		{"commit", "--quiet", "-m", "начальный коммит"},
	}
	for _, args := range commands {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		// Настройки пользователя не должны влиять на результат теста
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}

	return repo
}

// writeFile записывает файл, прерывая тест при ошибке
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("не удалось записать %s: %v", path, err)
	}
}
