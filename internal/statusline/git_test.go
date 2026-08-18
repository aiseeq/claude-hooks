package statusline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	if !status.RepoClean || status.Changed != 0 {
		t.Errorf("свежий коммит должен давать чистое дерево, получено %+v", status)
	}

	writeFile(t, filepath.Join(repo, "file.txt"), "изменение")

	status = gitStatus(t, context.Background(), repo)
	if status.Changed != 1 || status.RepoClean {
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
