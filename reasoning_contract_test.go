package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// The integration preserves these fields inside models.json's "upstream"
// object. Shelley must consume that object, not guess capabilities from the
// proxy hostname or from a similarly named model on another provider.
func TestMuseReasoningDiscoveryContract(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, modelList{Object: "list", Data: []model{
			{ID: "muse-spark-1.3-contributor", Object: "model", OwnedBy: "opencode"},
		}})
	}))
	defer upstream.Close()
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(slog.New(slog.NewTextHandler(io.Discard, nil)), u, "test-session")
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/models", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	var list modelList
	if err := json.Unmarshal(res.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("models = %+v", list.Data)
	}
	m := list.Data[0]
	if m.ID != "meta/muse-spark-1.3-contributor" || m.APIType != formatResponses || m.Endpoint != "/responses" {
		t.Fatalf("routing metadata = %+v", m)
	}
	if !m.SupportsReasoning || !reflect.DeepEqual(m.ReasoningLevels, []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("reasoning metadata = %+v", m)
	}
}

func TestReasoningPayloadSurvivesProxy(t *testing.T) {
	for _, tc := range []struct {
		name, path, upstreamPath, publicID, upstreamID, body string
	}{
		{
			"responses", "/responses", "/v1/responses",
			"meta/muse-spark-1.3-contributor", "muse-spark-1.3-contributor",
			`{"input":"test","stream":true,"reasoning":{"effort":"xhigh","summary":"auto"}}`,
		},
		{
			"chat", "/completions", "/v1/chat/completions",
			"z-ai/glm-5.3", "glm-5.3",
			`{"messages":[],"reasoning_effort":"high","stream":true}`,
		},
		{
			"messages", "/messages", "/v1/messages",
			"minimax/minimax-m3", "minimax-m3",
			`{"messages":[],"max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":2048}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var want map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
				t.Fatal(err)
			}
			want["model"], _ = json.Marshal(tc.publicID)
			body, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			want["model"], _ = json.Marshal(tc.upstreamID)
			called := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				if r.URL.Path != tc.upstreamPath {
					t.Errorf("upstream path = %q, want %q", r.URL.Path, tc.upstreamPath)
				}
				if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-OpenCode-Session") != "test-session" {
					t.Error("missing upstream auth or session identity")
				}
				if tc.name == "messages" && r.Header.Get("X-Api-Key") != "test-token" {
					t.Error("missing Anthropic auth normalization")
				}
				var got map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("payload changed beyond model ID: got %s, want %v", got, want)
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			}))
			defer upstream.Close()
			u, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			s := newServer(slog.New(slog.NewTextHandler(io.Discard, nil)), u, "test-session")
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer test-token")
			res := httptest.NewRecorder()
			s.routes().ServeHTTP(res, req)
			if res.Code != http.StatusOK || !called {
				t.Fatalf("status = %d, upstream called = %v: %s", res.Code, called, res.Body.String())
			}
		})
	}
}
