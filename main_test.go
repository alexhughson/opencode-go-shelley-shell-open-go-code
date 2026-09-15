package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestModelsAddsPerModelAPITypeAndSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected upstream path %q", r.URL.Path)
		}
		if got := r.Header.Get("X-OpenCode-Session"); got != "session-123" {
			t.Fatalf("session header = %q", got)
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

	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/models", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}

	var got modelList
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Data) != 3 {
		t.Fatalf("unexpected models: %#v", got.Data)
	}
	want := map[string]struct {
		api      apiFormat
		endpoint string
		levels   []string
	}{
		"gpt-5.6-luna": {formatResponses, "/responses", []string{"off", "low", "medium", "high", "xhigh", "max"}},
		"glm-5.3":      {formatChat, "/completions", nil},
		"minimax-m3":   {formatMessages, "/messages", []string{"none", "thinking"}},
	}
	for _, m := range got.Data {
		w := want[m.ID]
		if m.APIType != w.api || m.Endpoint != w.endpoint || !slices.Equal(m.ReasoningLevels, w.levels) {
			t.Fatalf("model %s has api_type=%q endpoint=%q levels=%q", m.ID, m.APIType, m.Endpoint, m.ReasoningLevels)
		}
	}
}

func TestSingleBaseRoutesAndSessionInjection(t *testing.T) {
	paths := make(chan string, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-OpenCode-Session"); got != "shelley-abc" {
			t.Fatalf("session header = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer forwarded" {
			t.Fatalf("authorization = %q", got)
		}
		paths <- r.URL.Path
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	s := newServer(slog.New(slog.NewTextHandler(io.Discard, nil)), u, "shelley-abc")

	for _, path := range []string{"/responses", "/completions", "/messages"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"anything"}`))
		req.Header.Set("Authorization", "Bearer forwarded")
		res := httptest.NewRecorder()
		s.routes().ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", path, res.Code, res.Body.String())
		}
	}

	want := []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"}
	for _, expected := range want {
		if got := <-paths; got != expected {
			t.Fatalf("upstream path = %q, want %q", got, expected)
		}
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
