package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Ошибка обновления называет только код ответа: тело может нести эхо токена
func TestRefreshErrorHidesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"server<%s>_error","detail":"%s"}`, refreshPrefix, refreshPrefix+"x")
	}))
	defer srv.Close()
	client := NewClient("test")
	client.TokenURL, client.HTTP = srv.URL, srv.Client()
	_, err := client.Refresh(context.Background(), oauth{RefreshToken: refreshPrefix + "x"}, time.Now())
	if err == nil || errors.Is(err, errDeadLogin) || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("ошибка: %v", err)
	}
	assertNoSecrets(t, "ошибка", err.Error())
}

// Сервер не сменил токен обновления: остаётся прежний, срок цепочки — из ответа
func TestRefreshKeepsRefreshToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"access_token":"new","expires_in":3600,"refresh_token_expires_in":86400,"scope":""}`)
	}))
	defer srv.Close()
	client := NewClient("test")
	client.TokenURL, client.HTTP = srv.URL, srv.Client()
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	got, err := client.Refresh(context.Background(), oauth{RefreshToken: "old", Scopes: []string{"user:inference"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "old" || !got.ExpiresAt.Equal(now.Add(time.Hour)) || !got.RefreshExpiresAt.Equal(now.Add(24*time.Hour)) || len(got.Scopes) != 1 {
		t.Errorf("ответ: токен прежний=%t, срок %v, цепочка %v, scopes %v", got.RefreshToken == "old", got.ExpiresAt, got.RefreshExpiresAt, got.Scopes)
	}
}

// Неначатое окно приходит без сброса; сброс округляется до минуты
func TestUsageWindows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fmt.Fprint(w, `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":95,"resets_at":"2026-10-26T09:59:59.950000+00:00"}}`)
	}))
	defer srv.Close()
	client := NewClient("test")
	client.APIBase, client.HTTP = srv.URL, srv.Client()
	got, err := client.Usage(context.Background(), "t", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !got.FiveHour.ResetsAt.IsZero() || got.Week.Used != 95 || !got.Week.ResetsAt.Equal(time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("окна: %+v", got)
	}
}
