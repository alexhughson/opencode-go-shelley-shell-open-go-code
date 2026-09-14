package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestScopedModelsOnlyAdvertiseCompatibleModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected upstream path %q", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, modelList{Object: "list", Data: []model{
			{ID: "gpt-5.6-luna", Object: "model", OwnedBy: "opencode"},
			{ID: "glm-5.3", Object: "model", OwnedBy: "opencode"},
			{ID: "minimax-m3", Object: "model", OwnedBy: "opencode"},
			{ID: "undocumented", Object: "model", OwnedBy: "opencode"},
		}})
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	s := newServer(slog.New(slog.NewTextHandler(io.Discard, nil)), u, "session-123")

	req := httptest.NewRequest(http.MethodGet, "/responses/v1/models", nil)
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var got modelList
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 1 || got.Data[0].ID != "gpt-5.6-luna" {
		t.Fatalf("unexpected models: %#v", got.Data)
	}
}

func TestNormalizeAnthropicBearerAuth(t *testing.T) {
	header := make(http.Header)
	header.Set("Authorization", "Bearer passed-through-token")
	normalizeAuth(header, formatMessages)
	if got := header.Get("X-Api-Key"); got != "passed-through-token" {
		t.Fatalf("x-api-key = %q", got)
	}
}

func TestProxyAddsSessionAndRejectsWrongFormat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected upstream path %q", r.URL.Path)
		}
		if got := r.Header.Get("X-OpenCode-Session"); got != "shelley-abc" {
			t.Fatalf("session header = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer forwarded" {
			t.Fatalf("authorization = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	s := newServer(slog.New(slog.NewTextHandler(io.Discard, nil)), u, "shelley-abc")
	h := s.routes()

	good := httptest.NewRequest(http.MethodPost, "/responses/v1/responses", strings.NewReader(`{"model":"gpt-5.6-luna","input":"hi"}`))
	good.Header.Set("Authorization", "Bearer forwarded")
	goodRes := httptest.NewRecorder()
	h.ServeHTTP(goodRes, good)
	if goodRes.Code != http.StatusOK {
		t.Fatalf("good status = %d, body = %s", goodRes.Code, goodRes.Body.String())
	}

	bad := httptest.NewRequest(http.MethodPost, "/responses/v1/responses", strings.NewReader(`{"model":"minimax-m3","input":"hi"}`))
	badRes := httptest.NewRecorder()
	h.ServeHTTP(badRes, bad)
	if badRes.Code != http.StatusBadRequest || !strings.Contains(badRes.Body.String(), "/v1/messages") {
		t.Fatalf("bad status = %d, body = %s", badRes.Code, badRes.Body.String())
	}
}
