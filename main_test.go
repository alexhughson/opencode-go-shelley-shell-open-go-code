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
		api       apiFormat
		endpoint  string
		levels    []string
		reasoning bool
	}{
		"openai/gpt-5.6-luna": {api: formatResponses, endpoint: "/responses", levels: []string{"off", "low", "medium", "high", "xhigh", "max"}, reasoning: true},
		"z-ai/glm-5.3":        {api: formatChat, endpoint: "/completions"},
		"minimax/minimax-m3":  {api: formatMessages, endpoint: "/messages", levels: []string{"none", "thinking"}, reasoning: true},
	}
	for _, m := range got.Data {
		w, ok := want[m.ID]
		if !ok {
			t.Fatalf("unexpected public model id %q", m.ID)
		}
		if m.APIType != w.api || m.Endpoint != w.endpoint || !slices.Equal(m.ReasoningLevels, w.levels) || m.SupportsReasoning != w.reasoning {
			t.Fatalf("model %s api_type=%q endpoint=%q levels=%q reasoning=%v", m.ID, m.APIType, m.Endpoint, m.ReasoningLevels, m.SupportsReasoning)
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

func TestForwardStripsPublicModelPrefix(t *testing.T) {
	bodies := make(chan string, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		bodies <- string(raw)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	s := newServer(slog.New(slog.NewTextHandler(io.Discard, nil)), u, "session-123")

	prefixed := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"openai/gpt-5.6-luna","input":"hi"}`))
	prefixed.Header.Set("Authorization", "Bearer forwarded")
	prefixedRes := httptest.NewRecorder()
	s.routes().ServeHTTP(prefixedRes, prefixed)
	if prefixedRes.Code != http.StatusOK {
		t.Fatalf("prefixed status = %d, body = %s", prefixedRes.Code, prefixedRes.Body.String())
	}
	var gotPrefixed map[string]any
	if err := json.Unmarshal([]byte(<-bodies), &gotPrefixed); err != nil {
		t.Fatal(err)
	}
	if gotPrefixed["model"] != "gpt-5.6-luna" {
		t.Fatalf("upstream model = %#v", gotPrefixed["model"])
	}

	legacy := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"opencode-go-gpt-5.6-luna"}`))
	legacy.Header.Set("Authorization", "Bearer forwarded")
	legacyRes := httptest.NewRecorder()
	s.routes().ServeHTTP(legacyRes, legacy)
	if legacyRes.Code != http.StatusOK {
		t.Fatalf("legacy status = %d, body = %s", legacyRes.Code, legacyRes.Body.String())
	}
	var gotLegacy map[string]any
	if err := json.Unmarshal([]byte(<-bodies), &gotLegacy); err != nil {
		t.Fatal(err)
	}
	if gotLegacy["model"] != "gpt-5.6-luna" {
		t.Fatalf("legacy upstream model = %#v", gotLegacy["model"])
	}

	plain := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"gpt-5.6-luna"}`))
	plain.Header.Set("Authorization", "Bearer forwarded")
	plainRes := httptest.NewRecorder()
	s.routes().ServeHTTP(plainRes, plain)
	if plainRes.Code != http.StatusOK {
		t.Fatalf("plain status = %d, body = %s", plainRes.Code, plainRes.Body.String())
	}
	if got := <-bodies; got != `{"model":"gpt-5.6-luna"}` {
		t.Fatalf("unprefixed body rewritten: %s", got)
	}
}

func TestForwardRejectsNonJSONBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not receive an invalid body")
	}))
	defer upstream.Close()

	u, _ := url.Parse(upstream.URL)
	s := newServer(slog.New(slog.NewTextHandler(io.Discard, nil)), u, "session-123")

	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader("not-json"))
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestPublicAndUpstreamModelID(t *testing.T) {
	for id := range modelFormats {
		if got := upstreamModelID(publicModelID(id)); got != id {
			t.Errorf("model ID round trip: %q became %q", id, got)
		}
	}
	if got := publicModelID("muse-spark-1.3-contributor"); got != "meta/muse-spark-1.3-contributor" {
		t.Fatalf("muse public id = %q", got)
	}
	if got := publicModelID("qwen3.8-max"); got != "opencode-go/qwen3.8-max" {
		t.Fatalf("qwen3.8-max public id = %q", got)
	}
	cases := map[string]string{
		"meta/muse-spark-1.3-contributor":        "muse-spark-1.3-contributor",
		"opencode-go/muse-spark-1.3-contributor": "muse-spark-1.3-contributor",
		"opencode-go-muse-spark-1.3-contributor": "muse-spark-1.3-contributor",
		"x-ai/grok-4.6":                          "grok-4.6",
		"gpt-5.6-luna":                           "gpt-5.6-luna",
		"unknown/model":                          "unknown/model",
	}
	for in, want := range cases {
		if got := upstreamModelID(in); got != want {
			t.Fatalf("upstreamModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRewritePublicModelID(t *testing.T) {
	got, err := rewritePublicModelID([]byte(`{"model":"minimax/minimax-m3","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "minimax-m3" {
		t.Fatalf("model = %#v", payload["model"])
	}
	if payload["stream"] != true {
		t.Fatalf("stream = %#v", payload["stream"])
	}

	legacy, err := rewritePublicModelID([]byte(`{"model":"opencode-go-minimax-m3"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(legacy, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["model"] != "minimax-m3" {
		t.Fatalf("legacy model = %#v", payload["model"])
	}

	unchanged := []byte(`{"model":"glm-5.3"}`)
	got, err = rewritePublicModelID(unchanged)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(unchanged) {
		t.Fatalf("rewrote unprefixed body: %s", got)
	}
}

func TestRewritePreservesOpaqueFields(t *testing.T) {
	body := []byte(`{"model":"minimax/minimax-m3","custom":{"model":"leave/me","id":9007199254740993},"thinking":{"type":"enabled","budget_tokens":2048},"stream":true}`)
	got, err := rewritePublicModelID(body)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(body, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &after); err != nil {
		t.Fatal(err)
	}
	before["model"] = json.RawMessage(`"minimax-m3"`)
	if len(before) != len(after) {
		t.Fatalf("fields changed: %s", got)
	}
	for key, value := range before {
		if string(after[key]) != string(value) {
			t.Errorf("field %s = %s, want %s", key, after[key], value)
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
