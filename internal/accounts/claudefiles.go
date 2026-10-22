package accounts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// oauthKey — поле файла токенов со входом claude.ai. Остальные поля файла
// (входы MCP-серверов, секреты плагинов) от учётки не зависят и при
// переключении остаются как есть
const oauthKey = "claudeAiOauth"

// oauthAccountKey — поле ~/.claude.json с учёткой входа
const oauthAccountKey = "oauthAccount"

// oauth — нужные ротации поля claudeAiOauth. Само значение хранится целиком
// (json.RawMessage): Claude Code пишет туда и другие поля
type oauth struct {
	AccessToken           string   `json:"accessToken"`
	RefreshToken          string   `json:"refreshToken"`
	ExpiresAt             int64    `json:"expiresAt"`
	RefreshTokenExpiresAt int64    `json:"refreshTokenExpiresAt"`
	Scopes                []string `json:"scopes"`
	ClientID              string   `json:"clientId"`
	RateLimitTier         string   `json:"rateLimitTier"`
}

func parseOAuth(raw json.RawMessage) (oauth, error) {
	var o oauth
	if len(raw) == 0 {
		return o, errors.New("no claudeAiOauth")
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return o, fmt.Errorf("claudeAiOauth is not an object: %w", err)
	}
	return o, nil
}

// cleared — Claude Code стёр токены: обновление получило invalid_grant,
// вход мёртв и нужен новый /login
func (o oauth) cleared() bool { return o.RefreshToken == "" }

// expires — когда истекает токен доступа
func (o oauth) expires() time.Time { return time.UnixMilli(o.ExpiresAt) }

// family — срок цепочки токенов обновления. Обновление меняет сам токен
// обновления, а срок цепочки остаётся сроком входа (на 2.1.305 четыре
// обновления подряд дали тот же срок): по нему вход узнаётся без сети
func (o oauth) family() int64 { return o.RefreshTokenExpiresAt }

// credentialsFile — файл токенов: поля верхнего уровня как есть и время
// изменения, по которому сессии Claude Code замечают подмену
type credentialsFile struct {
	fields map[string]json.RawMessage
	mtime  time.Time
}

func readCredentials(path string) (credentialsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return credentialsFile{}, fmt.Errorf("cannot read Claude Code credentials: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return credentialsFile{}, fmt.Errorf("cannot stat Claude Code credentials: %w", err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		// Текст ошибки разбора не печатается: в нём может оказаться кусок токена
		return credentialsFile{}, fmt.Errorf("Claude Code credentials %s are not a JSON object", path)
	}
	return credentialsFile{fields: fields, mtime: info.ModTime()}, nil
}

// oauthRaw — значение claudeAiOauth
func (c credentialsFile) oauthRaw() json.RawMessage { return c.fields[oauthKey] }

// writeCredentials заменяет claudeAiOauth, сохраняя прочие поля, и
// записывает файл атомарно с правами 600. Возвращает новое время изменения
func writeCredentials(path string, current credentialsFile, value json.RawMessage) (time.Time, error) {
	fields := make(map[string]json.RawMessage, len(current.fields)+1)
	for key, raw := range current.fields {
		fields[key] = raw
	}
	fields[oauthKey] = value
	data, err := json.Marshal(fields)
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot encode Claude Code credentials: %w", err)
	}
	if err := writeAtomic(path, data, 0o600); err != nil {
		return time.Time{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot stat Claude Code credentials: %w", err)
	}
	return info.ModTime(), nil
}

// writeAtomic пишет файл через временный рядом и переименование: читатель
// видит либо прежний файл, либо новый целиком
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("cannot create %s: %w", path, err)
	}
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("cannot replace %s: %w", path, err)
	}
	return nil
}

// accountRef — учётка из oauthAccount
type accountRef struct {
	AccountUUID      string `json:"accountUuid"`
	EmailAddress     string `json:"emailAddress"`
	OrganizationUUID string `json:"organizationUuid"`
}

// member — поле JSON-объекта верхнего уровня в порядке файла
type member struct {
	key   string
	value json.RawMessage
}

// readMembers разбирает объект верхнего уровня, сохраняя порядок полей и
// их текст: ~/.claude.json переписывается с заменой одного поля, остальное
// в нём остаётся байт в байт
func readMembers(data []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("cannot read the object start: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("not a JSON object")
	}
	var members []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("cannot read an object key: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("cannot read the value of %q: %w", key, err)
		}
		members = append(members, member{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("cannot read the object end: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("data after the JSON object")
	}
	return members, nil
}

// writeMembers собирает объект с отступом в два пробела, как пишет Claude Code
func writeMembers(members []member) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("{")
	for i, m := range members {
		key, err := json.Marshal(m.key)
		if err != nil {
			return nil, fmt.Errorf("cannot encode key %q: %w", m.key, err)
		}
		if i > 0 {
			buf.WriteString(",")
		}
		buf.WriteString("\n  ")
		buf.Write(key)
		buf.WriteString(": ")
		buf.Write(m.value)
	}
	if len(members) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString("}")
	return buf.Bytes(), nil
}

// readOAuthAccount читает oauthAccount из ~/.claude.json
func readOAuthAccount(path string) (json.RawMessage, accountRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, accountRef{}, fmt.Errorf("cannot read Claude Code config: %w", err)
	}
	members, err := readMembers(data)
	if err != nil {
		return nil, accountRef{}, fmt.Errorf("cannot parse Claude Code config %s: %w", path, err)
	}
	for _, m := range members {
		if m.key != oauthAccountKey {
			continue
		}
		var ref accountRef
		if err := json.Unmarshal(m.value, &ref); err != nil {
			return nil, accountRef{}, fmt.Errorf("oauthAccount in %s is not an object: %w", path, err)
		}
		return m.value, ref, nil
	}
	return nil, accountRef{}, nil
}

// replaceOAuthAccount заменяет oauthAccount в ~/.claude.json под
// блокировкой конфигурации Claude Code: сессии пишут файл, перечитывая его
// под той же блокировкой, и замену не затирают
func replaceOAuthAccount(p Paths, value json.RawMessage) error {
	release, err := dirLock(p.configLock(), configLockStale)
	if err != nil {
		return err
	}
	defer release()

	data, err := os.ReadFile(p.GlobalConfig)
	if err != nil {
		return fmt.Errorf("cannot read Claude Code config: %w", err)
	}
	info, err := os.Stat(p.GlobalConfig)
	if err != nil {
		return fmt.Errorf("cannot stat Claude Code config: %w", err)
	}
	members, err := readMembers(data)
	if err != nil {
		return fmt.Errorf("cannot parse Claude Code config %s: %w", p.GlobalConfig, err)
	}
	indented, err := indentMember(value)
	if err != nil {
		return err
	}
	replaced := false
	for i := range members {
		if members[i].key == oauthAccountKey {
			members[i].value = indented
			replaced = true
		}
	}
	if !replaced {
		members = append(members, member{key: oauthAccountKey, value: indented})
	}
	out, err := writeMembers(members)
	if err != nil {
		return fmt.Errorf("cannot encode Claude Code config: %w", err)
	}
	return writeAtomic(p.GlobalConfig, out, info.Mode().Perm())
}

// indentMember выравнивает значение под поле верхнего уровня
func indentMember(value json.RawMessage) (json.RawMessage, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, value, "  ", "  "); err != nil {
		return nil, fmt.Errorf("oauthAccount is not valid JSON: %w", err)
	}
	return buf.Bytes(), nil
}
