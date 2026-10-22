package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Адреса и клиент OAuth — те же, что у Claude Code 2.1.305
const (
	defaultAPIBase  = "https://api.anthropic.com"
	defaultTokenURL = "https://platform.claude.com/v1/oauth/token"
	defaultClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	usageBeta       = "oauth-2025-04-20"
)

// errDeadLogin — сервер отказал в обновлении (invalid_grant): токен
// обновления отозван или истёк, учётке нужен новый /login
var errDeadLogin = errors.New("refresh token rejected (invalid_grant)")

// Client — запросы к OAuth Anthropic. Тесты подставляют httptest-сервер
type Client struct {
	APIBase   string
	TokenURL  string
	ClientID  string
	UserAgent string
	HTTP      *http.Client
}

// NewClient — клиент с адресами Claude Code
func NewClient(userAgent string) *Client {
	return &Client{
		APIBase:   defaultAPIBase,
		TokenURL:  defaultTokenURL,
		ClientID:  defaultClientID,
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// profile — учётка токена
type profile struct {
	UUID    string
	Email   string
	OrgUUID string
}

// Profile спрашивает, чей токен доступа: /api/oauth/profile
func (c *Client) Profile(ctx context.Context, accessToken string) (profile, error) {
	var body struct {
		Account struct {
			UUID  string `json:"uuid"`
			Email string `json:"email"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if err := c.get(ctx, c.APIBase+"/api/oauth/profile", accessToken, nil, &body); err != nil {
		return profile{}, fmt.Errorf("profile: %w", err)
	}
	if body.Account.UUID == "" || body.Account.Email == "" {
		return profile{}, errors.New("profile: response has no account uuid or email")
	}
	return profile{UUID: body.Account.UUID, Email: body.Account.Email, OrgUUID: body.Organization.UUID}, nil
}

// Window — окно лимита: доля и сброс. Сброса нет — окно не начато
type Window struct {
	Used     float64
	ResetsAt time.Time
}

// usage — недельное и пятичасовое окна учётки
type usage struct {
	Week     Window
	FiveHour Window
	At       time.Time
}

// Usage читает расход учётки: /api/oauth/usage, тот же ответ видит /usage
func (c *Client) Usage(ctx context.Context, accessToken string, now time.Time) (usage, error) {
	type window struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    *string  `json:"resets_at"`
	}
	var body struct {
		FiveHour *window `json:"five_hour"`
		SevenDay *window `json:"seven_day"`
	}
	headers := map[string]string{"anthropic-beta": usageBeta}
	if err := c.get(ctx, c.APIBase+"/api/oauth/usage", accessToken, headers, &body); err != nil {
		return usage{}, fmt.Errorf("usage: %w", err)
	}
	convert := func(name string, w *window) (Window, error) {
		if w == nil || w.Utilization == nil {
			return Window{}, nil
		}
		out := Window{Used: *w.Utilization}
		if w.ResetsAt != nil && *w.ResetsAt != "" {
			at, err := time.Parse(time.RFC3339Nano, *w.ResetsAt)
			if err != nil {
				return Window{}, fmt.Errorf("usage: %s.resets_at %q: %w", name, *w.ResetsAt, err)
			}
			// Строка статуса получает сброс из заголовка в целых минутах
			// (11:00:00), API — за долю секунды до него (10:59:59.95)
			out.ResetsAt = at.Round(time.Minute)
		}
		if out.Used < 0 || math.IsNaN(out.Used) {
			return Window{}, fmt.Errorf("usage: %s.utilization %v", name, out.Used)
		}
		return out, nil
	}
	week, err := convert("seven_day", body.SevenDay)
	if err != nil {
		return usage{}, err
	}
	five, err := convert("five_hour", body.FiveHour)
	if err != nil {
		return usage{}, err
	}
	return usage{Week: week, FiveHour: five, At: now}, nil
}

// refreshed — ответ обновления токена
type refreshed struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
	Scopes           []string
	AccountUUID      string
}

// Refresh обновляет токен так же, как Claude Code: grant_type refresh_token
// с прежними scopes. Сервер выдаёт новый токен обновления, старый после
// этого не годится — вызывающий обязан сохранить ответ
func (c *Client) Refresh(ctx context.Context, o oauth, now time.Time) (refreshed, error) {
	clientID := o.ClientID
	if clientID == "" {
		clientID = c.ClientID
	}
	payload, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": o.RefreshToken,
		"client_id":     clientID,
		"scope":         strings.Join(o.Scopes, " "),
	})
	if err != nil {
		return refreshed{}, fmt.Errorf("refresh: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, strings.NewReader(string(payload)))
	if err != nil {
		return refreshed{}, fmt.Errorf("refresh: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return refreshed{}, fmt.Errorf("refresh: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return refreshed{}, fmt.Errorf("refresh: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var fail struct {
			Code string `json:"error"`
		}
		if err := json.Unmarshal(data, &fail); err != nil {
			// Тело ответа не печатается: в нём может быть эхо запроса
			return refreshed{}, fmt.Errorf("refresh: HTTP %d, body is not JSON: %w", resp.StatusCode, err)
		}
		if fail.Code == "invalid_grant" {
			return refreshed{}, errDeadLogin
		}
		return refreshed{}, fmt.Errorf("refresh: HTTP %d %s", resp.StatusCode, safeCode(fail.Code))
	}
	var body struct {
		AccessToken           string `json:"access_token"`
		RefreshToken          string `json:"refresh_token"`
		ExpiresIn             int64  `json:"expires_in"`
		RefreshTokenExpiresIn *int64 `json:"refresh_token_expires_in"`
		Scope                 string `json:"scope"`
		Account               *struct {
			UUID string `json:"uuid"`
		} `json:"account"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return refreshed{}, fmt.Errorf("refresh: response is not JSON: %w", err)
	}
	if body.AccessToken == "" || body.ExpiresIn <= 0 {
		return refreshed{}, errors.New("refresh: response has no access token or expiry")
	}
	out := refreshed{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(body.ExpiresIn) * time.Second),
		Scopes:       strings.Fields(body.Scope),
	}
	if out.RefreshToken == "" {
		// Сервер не сменил токен обновления: прежний остаётся в силе
		out.RefreshToken = o.RefreshToken
	}
	if body.RefreshTokenExpiresIn != nil {
		out.RefreshExpiresAt = now.Add(time.Duration(*body.RefreshTokenExpiresIn) * time.Second)
	}
	if len(out.Scopes) == 0 {
		out.Scopes = o.Scopes
	}
	if body.Account != nil {
		out.AccountUUID = body.Account.UUID
	}
	return out, nil
}

// get — GET с токеном доступа и разбором JSON
func (c *Client) get(ctx context.Context, url, accessToken string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("cannot build the request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("cannot read the response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("response is not JSON: %w", err)
	}
	return nil
}

// oauthErrors — коды ошибок OAuth (RFC 6749, 5.2) и сервера, которые
// безопасно печатать
var oauthErrors = map[string]bool{
	"invalid_request": true, "invalid_client": true, "unauthorized_client": true,
	"unsupported_grant_type": true, "invalid_scope": true, "server_error": true,
	"temporarily_unavailable": true, "rate_limit_error": true, "access_denied": true,
}

// safeCode — код ошибки сервера, если он из известных. Иной код не
// печатается: поле могло нести эхо токена
func safeCode(code string) string {
	if oauthErrors[code] {
		return code
	}
	if code == "" {
		return "(no error code)"
	}
	return "(unknown error code)"
}
