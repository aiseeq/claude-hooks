package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiseeq/claude-hooks/internal/core"
)

// Токены фикстуры узнаются по приставкам: ни одна не должна попасть в лог,
// вывод или текст ошибки
const (
	accessPrefix  = "tokA-"
	refreshPrefix = "tokR-"
)

// person — учётка фикстуры
type person struct {
	name   string
	uuid   string
	email  string
	family time.Time
}

var (
	work     = person{name: "work", uuid: "u-work", email: "work@example.com"}
	personal = person{name: "personal", uuid: "u-personal", email: "personal@example.com"}
)

// fixture — каталоги Claude Code и хранилища во временной папке и
// httptest-сервер OAuth вместо Anthropic
type fixture struct {
	t       *testing.T
	p       Paths
	r       *Rotator
	limits  *fakeLimits
	logFile string
	now     time.Time

	mu        sync.Mutex
	people    map[string]person
	usage     map[string]string
	dead      map[string]bool
	profiles  int
	refreshes int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	f := &fixture{
		t: t,
		p: Paths{
			ClaudeDir:    filepath.Join(dir, "claude"),
			GlobalConfig: filepath.Join(dir, "claude.json"),
			StateDir:     filepath.Join(dir, "state"),
		},
		limits:  &fakeLimits{usage: map[string]Usage{}},
		logFile: filepath.Join(dir, "hooks.log"),
		now:     now,
		people:  map[string]person{},
		usage:   map[string]string{},
		dead:    map[string]bool{},
	}
	for _, who := range []*person{&work, &personal} {
		p := *who
		// Свой срок цепочки у каждой учётки: по нему вход узнаётся без сети
		p.family = now.Add(30 * 24 * time.Hour)
		if p.name == "personal" {
			p.family = now.Add(29 * 24 * time.Hour)
		}
		f.people[p.name] = p
	}
	if err := os.MkdirAll(f.p.ClaudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	logger, err := core.NewLogger(core.LoggerConfig{Level: "debug", Format: "text", Output: "file", LogFile: f.logFile})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient("test")
	client.APIBase, client.TokenURL, client.HTTP = srv.URL, srv.URL+"/token", srv.Client()
	f.r = &Rotator{Paths: f.p, Client: client, Limits: f.limits, Log: logger, Now: func() time.Time { return f.now }}
	f.writeConfig(work)
	t.Cleanup(f.assertNoSecretsInLog)
	return f
}

func (f *fixture) person(name string) person {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.people[name]
}

// login — claudeAiOauth учётки: поколение gen, токен доступа живёт ttl
func (f *fixture) login(name string, gen int, ttl time.Duration) json.RawMessage {
	f.t.Helper()
	p := f.person(name)
	data, err := json.Marshal(map[string]any{
		"accessToken":           fmt.Sprintf("%s%s-%d", accessPrefix, name, gen),
		"refreshToken":          fmt.Sprintf("%s%s-%d", refreshPrefix, name, gen),
		"expiresAt":             f.now.Add(ttl).UnixMilli(),
		"refreshTokenExpiresAt": p.family.UnixMilli(),
		"scopes":                []string{"user:inference", "user:profile"},
		"subscriptionType":      "max",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return data
}

// writeCreds пишет файл токенов, как Claude Code: вход плюс чужие поля.
// Время изменения — минуту назад, чтобы сверка не ждала settle
func (f *fixture) writeCreds(oauth json.RawMessage) {
	f.t.Helper()
	fields := map[string]json.RawMessage{"mcpOAuth": json.RawMessage(`{"srv":{"accessToken":"mcp-value"}}`)}
	if oauth != nil {
		fields[oauthKey] = oauth
	}
	data, err := json.Marshal(fields)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.p.Credentials(), data, 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.age(f.p.Credentials(), time.Minute)
}

// age сдвигает время изменения файла в прошлое
func (f *fixture) age(path string, by time.Duration) {
	f.t.Helper()
	at := time.Now().Add(-by)
	if err := os.Chtimes(path, at, at); err != nil {
		f.t.Fatal(err)
	}
}

// writeConfig пишет ~/.claude.json с oauthAccount учётки, как Claude Code
func (f *fixture) writeConfig(p person) {
	f.t.Helper()
	config := fmt.Sprintf("{\n  \"numStartups\": 5,\n  \"oauthAccount\": {\n    \"accountUuid\": %q,\n    \"emailAddress\": %q,\n    \"displayName\": %q\n  },\n  \"zz\": true\n}",
		p.uuid, p.email, p.name)
	if err := os.WriteFile(f.p.GlobalConfig, []byte(config), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// creds — claudeAiOauth и прочие поля файла токенов
func (f *fixture) creds() (oauth, map[string]json.RawMessage) {
	f.t.Helper()
	c, err := readCredentials(f.p.Credentials())
	if err != nil {
		f.t.Fatal(err)
	}
	o, err := parseOAuth(c.oauthRaw())
	if err != nil {
		f.t.Fatal(err)
	}
	return o, c.fields
}

func (f *fixture) store() *Store {
	f.t.Helper()
	s, err := loadStore(f.p)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

// save — /login учётки и её сохранение: токены в файле, затем сверка
func (f *fixture) save(name string) {
	f.t.Helper()
	f.writeConfig(f.person(name))
	f.writeCreds(f.login(name, 1, 8*time.Hour))
	if _, err := f.r.Rotate(context.Background(), Options{Force: true, Wait: true}); err != nil {
		f.t.Fatalf("сохранение %s: %v", name, err)
	}
}

// storedRefresh — токен обновления учётки в хранилище
func (f *fixture) storedRefresh(email string) string {
	f.t.Helper()
	a, err := f.store().find(email)
	if err != nil {
		f.t.Fatal(err)
	}
	o, err := a.oauth()
	if err != nil {
		f.t.Fatal(err)
	}
	return o.RefreshToken
}

// serve — OAuth Anthropic: профиль, расход и обновление токена
func (f *fixture) serve(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.URL.Path {
	case "/api/oauth/profile", "/api/oauth/usage":
		p, ok := f.byToken(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "+accessPrefix))
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if req.URL.Path == "/api/oauth/profile" {
			f.profiles++
			fmt.Fprintf(w, `{"account":{"uuid":%q,"email":%q},"organization":{"uuid":"org-%s"}}`, p.uuid, p.email, p.name)
			return
		}
		if req.Header.Get("anthropic-beta") != usageBeta {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, f.usage[p.name])
	case "/token":
		f.refreshes++
		var body map[string]string
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body["grant_type"] != "refresh_token" || body["client_id"] != defaultClientID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		token := strings.TrimPrefix(body["refresh_token"], refreshPrefix)
		p, ok := f.byToken(token)
		if !ok || f.dead[p.name] {
			// Сервер отвечает эхом токена: оно не должно дойти до ошибки
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":"invalid_grant","error_description":"bad %s"}`, body["refresh_token"])
			return
		}
		var gen int
		fmt.Sscanf(token[len(p.name)+1:], "%d", &gen)
		fmt.Fprintf(w, `{"access_token":"%s%s-%d","refresh_token":"%s%s-%d","expires_in":28800,"scope":%q,"account":{"uuid":%q}}`,
			accessPrefix, p.name, gen+1, refreshPrefix, p.name, gen+1, body["scope"], p.uuid)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// byToken — учётка по токену без приставки: «имя-поколение»
func (f *fixture) byToken(token string) (person, bool) {
	name, _, ok := strings.Cut(token, "-")
	if !ok {
		return person{}, false
	}
	p, ok := f.people[name]
	return p, ok
}

// assertNoSecretsInLog — токены не попали в лог
func (f *fixture) assertNoSecretsInLog() {
	data, err := os.ReadFile(f.logFile)
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	assertNoSecrets(f.t, "лог", string(data))
}

func assertNoSecrets(t *testing.T, where, text string) {
	t.Helper()
	for _, prefix := range []string{accessPrefix, refreshPrefix, "mcp-value"} {
		if strings.Contains(text, prefix) {
			t.Errorf("%s содержит токен (%s…)", where, prefix)
		}
	}
}

// fakeLimits — снимок строки статуса в памяти
type fakeLimits struct {
	mu    sync.Mutex
	usage map[string]Usage
}

func (l *fakeLimits) Usage() (map[string]Usage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]Usage, len(l.usage))
	for k, v := range l.usage {
		out[k] = v
	}
	return out, nil
}

func (l *fakeLimits) Record(email string, week, five Window, _ time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.usage[email] = Usage{Email: email, Week: week, FiveHour: five}
	return nil
}

func (l *fakeLimits) set(email string, week float64, resets time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.usage[email] = Usage{Email: email, Week: Window{Used: week, ResetsAt: resets}}
}
