package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const upstreamBase = "https://opencode.ai/zen/go"

type apiFormat string

const (
	formatResponses apiFormat = "openai-responses"
	formatChat      apiFormat = "openai-chat-completions"
	formatMessages  apiFormat = "anthropic-messages"
)

// OpenCode Go's model endpoint omits the wire format each model requires.
// These assignments follow the model table in the OpenCode Go documentation.
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
	APIType  apiFormat `json:"api_type"`
	Endpoint string    `json:"endpoint"`
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

type requestIDKey struct{}

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
		setUpstreamIdentity(req.Header, s.sessionID)
		req.Host = upstream.Host
		s.log.Info("forwarding upstream",
			"request_id", requestIDFrom(req.Context()),
			"method", req.Method,
			"url", req.URL.String(),
			"content_type", req.Header.Get("Content-Type"),
			"content_length", req.ContentLength,
			"authorization_present", req.Header.Get("Authorization") != "",
			"x_api_key_present", req.Header.Get("X-Api-Key") != "",
			"anthropic_version", req.Header.Get("Anthropic-Version"),
		)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		fields := []any{
			"request_id", requestIDFrom(resp.Request.Context()),
			"status", resp.StatusCode,
			"content_type", resp.Header.Get("Content-Type"),
			"content_length", resp.ContentLength,
		}
		if resp.StatusCode >= 400 {
			body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			if err == nil {
				_ = resp.Body.Close()
				resp.Body = io.NopCloser(strings.NewReader(string(body)))
				resp.ContentLength = int64(len(body))
				fields = append(fields, "error_body", strings.TrimSpace(string(body)))
			}
			s.log.Error("upstream response", fields...)
		} else {
			s.log.Info("upstream response", fields...)
		}
		return nil
	}
	proxy.Transport = transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		s.log.Error("upstream request failed", "request_id", requestIDFrom(r.Context()), "method", r.Method, "path", r.URL.Path, "error", err)
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

	// exe.dev's LLM integration appends these paths to one configured base URL.
	mux.HandleFunc("GET /models", s.models)
	mux.HandleFunc("POST /responses", s.forward(formatResponses, "/v1/responses"))
	mux.HandleFunc("POST /completions", s.forward(formatChat, "/v1/chat/completions"))
	mux.HandleFunc("POST /chat/completions", s.forward(formatChat, "/v1/chat/completions"))
	mux.HandleFunc("POST /messages", s.forward(formatMessages, "/v1/messages"))

	// Also accept conventional /v1 paths for direct clients and provider probes.
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/responses", s.forward(formatResponses, "/v1/responses"))
	mux.HandleFunc("POST /v1/completions", s.forward(formatChat, "/v1/chat/completions"))
	mux.HandleFunc("POST /v1/chat/completions", s.forward(formatChat, "/v1/chat/completions"))
	mux.HandleFunc("POST /v1/messages", s.forward(formatMessages, "/v1/messages"))

	return requestLogger(s.log, mux)
}

func (s *server) models(w http.ResponseWriter, r *http.Request) {
	models, err := s.fetchModels(r)
	if err != nil {
		s.log.Error("model discovery failed", "error", err)
		writeError(w, http.StatusBadGateway, "could not fetch upstream models")
		return
	}
	for i := range models.Data {
		format, ok := modelFormats[models.Data[i].ID]
		if !ok {
			continue
		}
		models.Data[i].APIType = format
		models.Data[i].Endpoint = endpointFor(format)
	}
	models.Data = slices.DeleteFunc(models.Data, func(m model) bool {
		_, documented := modelFormats[m.ID]
		return !documented
	})
	writeJSON(w, http.StatusOK, models)
}

func (s *server) fetchModels(r *http.Request) (modelList, error) {
	u := *s.upstream
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return modelList{}, err
	}
	copyAuthHeaders(req.Header, r.Header)
	setUpstreamIdentity(req.Header, s.sessionID)

	s.log.Info("fetching upstream models",
		"request_id", requestIDFrom(r.Context()),
		"url", u.String(),
		"authorization_present", req.Header.Get("Authorization") != "",
		"x_api_key_present", req.Header.Get("X-Api-Key") != "",
	)
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Error("upstream models request failed", "request_id", requestIDFrom(r.Context()), "error", err)
		return modelList{}, err
	}
	defer resp.Body.Close()
	s.log.Info("upstream models response", "request_id", requestIDFrom(r.Context()), "status", resp.StatusCode, "content_type", resp.Header.Get("Content-Type"), "content_length", resp.ContentLength)
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

func (s *server) forward(format apiFormat, upstreamPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		normalizeAuth(r.Header, format)
		r.URL.Path = upstreamPath
		s.proxy.ServeHTTP(w, r)
	}
}

func setUpstreamIdentity(header http.Header, sessionID string) {
	header.Set("X-OpenCode-Session", sessionID)
	header.Set("User-Agent", "exe.dev-opencode-go-proxy/1.0")
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
		return "/responses"
	case formatChat:
		return "/completions"
	case formatMessages:
		return "/messages"
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
	var sequence atomic.Uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		incomingPath := r.URL.Path
		id := fmt.Sprintf("proxy-%d-%d", started.UnixMilli(), sequence.Add(1))
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		w.Header().Set("X-Proxy-Request-ID", id)
		tracked := &statusWriter{ResponseWriter: w}

		log.Info("incoming request",
			"request_id", id,
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"host", r.Host,
			"content_type", r.Header.Get("Content-Type"),
			"content_length", r.ContentLength,
			"user_agent", r.Header.Get("User-Agent"),
			"x_forwarded_for", r.Header.Get("X-Forwarded-For"),
			"x_forwarded_host", r.Header.Get("X-Forwarded-Host"),
			"authorization_present", r.Header.Get("Authorization") != "",
			"x_api_key_present", r.Header.Get("X-Api-Key") != "",
			"anthropic_version", r.Header.Get("Anthropic-Version"),
			"header_names", headerNames(r.Header),
		)
		next.ServeHTTP(tracked, r)
		log.Info("request completed",
			"request_id", id,
			"method", r.Method,
			"path", incomingPath,
			"status", tracked.statusCode(),
			"response_bytes", tracked.bytes,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func headerNames(header http.Header) []string {
	names := make([]string, 0, len(header))
	for name := range header {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	return names
}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
