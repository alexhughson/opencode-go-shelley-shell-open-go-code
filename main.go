package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

const upstreamBase = "https://opencode.ai/zen/go"

type apiFormat string

const (
	formatResponses apiFormat = "openai_responses"
	formatChat      apiFormat = "openai_chat_completions"
	formatMessages  apiFormat = "anthropic_messages"
)

// OpenCode Go's /v1/models endpoint does not say which wire format each model
// accepts. This list follows the model table in the OpenCode Go documentation.
var modelFormats = map[string]apiFormat{
	"grok-4.6":                     formatResponses,
	"gpt-5.6-luna":                 formatResponses,
	"muse-spark-1.3-contributor":   formatResponses,
	"muse-spark-1.2-contributor":   formatResponses,
	"glm-5.3-flash":                formatChat,
	"glm-5.3":                      formatChat,
	"glm-5.2":                      formatChat,
	"glm-5.1":                      formatChat,
	"kimi-k3":                      formatChat,
	"kimi-k2.7-code":               formatChat,
	"kimi-k2.6":                    formatChat,
	"longcat-2.0":                  formatChat,
	"deepseek-v4.1-flash":          formatChat,
	"deepseek-v4-pro":              formatChat,
	"deepseek-v4-flash":            formatChat,
	"deepseek-v4-flash-vision-exp": formatChat,
	"mimo-v2.5":                    formatChat,
	"mimo-v2.5-pro":                formatChat,
	"hy4-preview":                  formatChat,
	"hy3":                          formatChat,
	"minimax-m3":                   formatMessages,
	"minimax-m2.7":                 formatMessages,
	"minimax-m2.5":                 formatMessages,
	"qwen3.8-max":                  formatMessages,
	"qwen3.8-flash":                formatMessages,
	"qwen3.7-max":                  formatMessages,
	"qwen3.7-plus":                 formatMessages,
	"qwen3.6-plus":                 formatMessages,
}

type model struct {
	ID       string    `json:"id"`
	Object   string    `json:"object"`
	Created  int64     `json:"created,omitempty"`
	OwnedBy  string    `json:"owned_by"`
	APIType  apiFormat `json:"api_type,omitempty"`
	Endpoint string    `json:"endpoint,omitempty"`
}

type modelList struct {
	Object string  `json:"object"`
	Data   []model `json:"data"`
}

type server struct {
	log       *slog.Logger
	sessionID string
	upstream  *url.URL
	client    *http.Client
	proxy     *httputil.ReverseProxy
}

func main() {
	listen := flag.String("listen", envOr("LISTEN_ADDR", ":8000"), "HTTP listen address")
	session := flag.String("session", os.Getenv("SHELLEY_CONVERSATION_ID"), "stable OpenCode session ID")
	flag.Parse()

	if strings.TrimSpace(*session) == "" {
		fmt.Fprintln(os.Stderr, "missing session ID: set SHELLEY_CONVERSATION_ID or pass -session")
		os.Exit(2)
	}

	upstream, err := url.Parse(upstreamBase)
	if err != nil {
		panic(err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s := newServer(log, upstream, *session)

	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	log.Info("starting proxy", "listen", *listen, "upstream", upstream.String(), "session", *session)
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func newServer(log *slog.Logger, upstream *url.URL, sessionID string) *server {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 5 * time.Minute

	s := &server{
		log:       log,
		sessionID: sessionID,
		upstream:  upstream,
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Header.Set("X-OpenCode-Session", s.sessionID)
		req.Header.Set("User-Agent", "exe.dev-opencode-go-proxy/1.0")
		req.Host = upstream.Host
	}
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		s.log.Error("upstream request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusBadGateway, "upstream request failed")
	}
	s.proxy = proxy
	return s
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /v1/models", s.modelsAll)
	mux.HandleFunc("POST /v1/responses", s.proxyFormat(formatResponses, "/v1/responses"))
	mux.HandleFunc("POST /v1/chat/completions", s.proxyFormat(formatChat, "/v1/chat/completions"))
	mux.HandleFunc("POST /v1/messages", s.proxyFormat(formatMessages, "/v1/messages"))

	for _, scoped := range []struct {
		prefix string
		format apiFormat
		path   string
	}{
		{"/responses", formatResponses, "/v1/responses"},
		{"/chat", formatChat, "/v1/chat/completions"},
		{"/anthropic", formatMessages, "/v1/messages"},
	} {
		mux.HandleFunc("GET "+scoped.prefix+"/v1/models", s.modelsFor(scoped.format))
		mux.HandleFunc("POST "+scoped.prefix+scoped.path, s.proxyFormat(scoped.format, scoped.path))
	}

	return requestLogger(s.log, mux)
}

func (s *server) modelsAll(w http.ResponseWriter, r *http.Request) {
	models, err := s.fetchModels(r)
	if err != nil {
		s.log.Error("model discovery failed", "error", err)
		writeError(w, http.StatusBadGateway, "could not fetch upstream models")
		return
	}
	for i := range models.Data {
		f, ok := modelFormats[models.Data[i].ID]
		if !ok {
			continue
		}
		models.Data[i].APIType = f
		models.Data[i].Endpoint = endpointFor(f)
	}
	models.Data = slices.DeleteFunc(models.Data, func(m model) bool {
		_, ok := modelFormats[m.ID]
		return !ok
	})
	writeJSON(w, http.StatusOK, models)
}

func (s *server) modelsFor(format apiFormat) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		models, err := s.fetchModels(r)
		if err != nil {
			s.log.Error("model discovery failed", "format", format, "error", err)
			writeError(w, http.StatusBadGateway, "could not fetch upstream models")
			return
		}
		models.Data = slices.DeleteFunc(models.Data, func(m model) bool {
			return modelFormats[m.ID] != format
		})
		writeJSON(w, http.StatusOK, models)
	}
}

func (s *server) fetchModels(r *http.Request) (modelList, error) {
	u := *s.upstream
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return modelList{}, err
	}
	copyAuthHeaders(req.Header, r.Header)
	req.Header.Set("X-OpenCode-Session", s.sessionID)
	req.Header.Set("User-Agent", "exe.dev-opencode-go-proxy/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return modelList{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return modelList{}, fmt.Errorf("upstream returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var models modelList
	if err := json.NewDecoder(resp.Body).Decode(&models); err != nil {
		return modelList{}, err
	}
	return models, nil
}

func (s *server) proxyFormat(format apiFormat, upstreamPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not read request body")
			return
		}
		var input struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &input); err != nil {
			writeError(w, http.StatusBadRequest, "request body must be JSON")
			return
		}
		actual, known := modelFormats[input.Model]
		if !known {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("model %q is not in the documented OpenCode Go model table", input.Model))
			return
		}
		if actual != format {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("model %q requires %s at %s", input.Model, actual, endpointFor(actual)))
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		normalizeAuth(r.Header, format)
		r.URL.Path = upstreamPath
		s.proxy.ServeHTTP(w, r)
	}
}

func normalizeAuth(header http.Header, format apiFormat) {
	switch format {
	case formatMessages:
		if header.Get("X-Api-Key") == "" {
			if scheme, token, ok := strings.Cut(header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
				header.Set("X-Api-Key", token)
			}
		}
	case formatResponses, formatChat:
		if header.Get("Authorization") == "" && header.Get("X-Api-Key") != "" {
			header.Set("Authorization", "Bearer "+header.Get("X-Api-Key"))
		}
	}
}

func endpointFor(format apiFormat) string {
	switch format {
	case formatResponses:
		return "/v1/responses"
	case formatChat:
		return "/v1/chat/completions"
	case formatMessages:
		return "/v1/messages"
	default:
		return ""
	}
}

func copyAuthHeaders(dst, src http.Header) {
	for _, name := range []string{"Authorization", "X-Api-Key", "Anthropic-Version", "Anthropic-Beta"} {
		if value := src.Values(name); len(value) > 0 {
			dst.Del(name)
			for _, v := range value {
				dst.Add(name, v)
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
		},
	})
}

func requestLogger(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Info("request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
