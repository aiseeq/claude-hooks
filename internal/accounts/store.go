package accounts

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// Store — хранилище учёток. Файл с правами 600: в нём токены всех подписок
type Store struct {
	// Active — uuid учётки, чьи токены сейчас в файле Claude Code
	Active string `json:"active,omitempty"`
	// SyncedMtime — время изменения файла токенов при последней сверке:
	// пока оно то же, файл сверять не нужно
	SyncedMtime time.Time `json:"syncedMtime,omitempty"`
	// AutoOff — автоматическое переключение выключено (ручным switch или auto off)
	AutoOff bool `json:"autoOff,omitempty"`
	// LastSwitch — последнее переключение: после него ротация выжидает
	LastSwitch time.Time `json:"lastSwitch,omitempty"`
	// RetryAt — сетевой шаг сверки не удался, до этого момента не повторять
	RetryAt  time.Time `json:"retryAt,omitempty"`
	Accounts []Account `json:"accounts"`
}

// Account — учётка подписки
type Account struct {
	UUID    string `json:"uuid"`
	Email   string `json:"email"`
	OrgUUID string `json:"organizationUuid,omitempty"`
	// OAuthAccount — копия oauthAccount из ~/.claude.json для этой учётки
	OAuthAccount json.RawMessage `json:"oauthAccount,omitempty"`
	// OAuth — claudeAiOauth целиком: секрет
	OAuth   json.RawMessage `json:"claudeAiOauth"`
	SavedAt time.Time       `json:"savedAt"`
	// Dead — почему вход учётки мёртв; пусто — жив
	Dead string `json:"dead,omitempty"`
	// UsageCheckedAt — когда расход учётки последний раз спрашивался у API
	UsageCheckedAt time.Time `json:"usageCheckedAt,omitempty"`
}

func (a *Account) oauth() (oauth, error) { return parseOAuth(a.OAuth) }

// loadStore читает хранилище. Файла нет — хранилище пустое
func loadStore(p Paths) (*Store, error) {
	data, err := os.ReadFile(p.Store())
	if errors.Is(err, fs.ErrNotExist) {
		return &Store{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read the accounts store: %w", err)
	}
	var s Store
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("accounts store %s is not valid JSON", p.Store())
	}
	return &s, nil
}

// saveStore пишет хранилище атомарно с правами 600
func saveStore(p Paths, s *Store) error {
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		return fmt.Errorf("cannot create the state directory: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode the accounts store: %w", err)
	}
	return writeAtomic(p.Store(), data, 0o600)
}

// byUUID — учётка по uuid или nil
func (s *Store) byUUID(uuid string) *Account {
	for i := range s.Accounts {
		if s.Accounts[i].UUID == uuid {
			return &s.Accounts[i]
		}
	}
	return nil
}

// find — учётка по почте или uuid, без учёта регистра почты
func (s *Store) find(name string) (*Account, error) {
	var found *Account
	for i := range s.Accounts {
		a := &s.Accounts[i]
		if a.UUID == name || strings.EqualFold(a.Email, name) {
			if found != nil {
				return nil, fmt.Errorf("%q matches several accounts", name)
			}
			found = a
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no saved account %q", name)
	}
	return found, nil
}

// byLogin — учётка, чей вход лежит в файле Claude Code: тот же токен
// обновления или та же цепочка (срок цепочки не меняется при обновлении).
// errUnknownLogin — вход хранилищу не известен
func (s *Store) byLogin(o oauth) (*Account, error) {
	stored := make([]oauth, len(s.Accounts))
	for i := range s.Accounts {
		parsed, err := s.Accounts[i].oauth()
		if err != nil {
			return nil, fmt.Errorf("stored login of %s: %w", s.Accounts[i].Email, err)
		}
		stored[i] = parsed
		if sameSecret(parsed.RefreshToken, o.RefreshToken) {
			return &s.Accounts[i], nil
		}
	}
	if o.family() == 0 {
		return nil, errUnknownLogin
	}
	var found *Account
	for i := range s.Accounts {
		if stored[i].family() != o.family() {
			continue
		}
		if found != nil {
			// Две учётки с одним сроком цепочки — вход по сроку не узнать
			return nil, errUnknownLogin
		}
		found = &s.Accounts[i]
	}
	if found == nil {
		return nil, errUnknownLogin
	}
	return found, nil
}

// errUnknownLogin — во входе нет ни токена, ни цепочки учётки хранилища
var errUnknownLogin = errors.New("login not in the accounts store")

// sameSecret сравнивает непустые токены за постоянное время
func sameSecret(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ActiveEmail — почта учётки, чьи токены сейчас у Claude Code. Пусто, если
// учёток нет или активная не опознана
func (s *Store) ActiveEmail() string {
	if s == nil {
		return ""
	}
	if a := s.byUUID(s.Active); a != nil {
		return a.Email
	}
	return ""
}

// applyRefreshed вписывает обновлённые токены в claudeAiOauth, сохраняя
// прочие поля
func applyRefreshed(raw json.RawMessage, r refreshed) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("claudeAiOauth is not an object: %w", err)
	}
	set := func(key string, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		fields[key] = data
		return nil
	}
	updates := []struct {
		key   string
		value any
	}{
		{"accessToken", r.AccessToken},
		{"refreshToken", r.RefreshToken},
		{"expiresAt", r.ExpiresAt.UnixMilli()},
		{"scopes", r.Scopes},
	}
	if !r.RefreshExpiresAt.IsZero() {
		updates = append(updates, struct {
			key   string
			value any
		}{"refreshTokenExpiresAt", r.RefreshExpiresAt.UnixMilli()})
	}
	for _, u := range updates {
		if err := set(u.key, u.value); err != nil {
			return nil, fmt.Errorf("cannot encode claudeAiOauth.%s: %w", u.key, err)
		}
	}
	data, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("cannot encode claudeAiOauth: %w", err)
	}
	return data, nil
}

// Info — учётка для сводок, без токенов
type Info struct {
	Email  string
	Active bool
	Dead   string
	// LoginExpires — когда истекает цепочка токенов: до этого нужен /login
	LoginExpires time.Time
}

// Infos — учётки хранилища в его порядке
func (s *Store) Infos() []Info {
	out := make([]Info, 0, len(s.Accounts))
	for i := range s.Accounts {
		a := &s.Accounts[i]
		info := Info{Email: a.Email, Active: a.UUID == s.Active, Dead: a.Dead}
		if o, err := a.oauth(); err == nil && o.family() > 0 {
			info.LoginExpires = time.UnixMilli(o.family())
		}
		out = append(out, info)
	}
	return out
}
