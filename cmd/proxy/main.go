package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yohanesgre/android-studio-llm-proxy/internal/cache"
	"github.com/yohanesgre/android-studio-llm-proxy/internal/config"
	"github.com/yohanesgre/android-studio-llm-proxy/internal/forward"
	"github.com/yohanesgre/android-studio-llm-proxy/internal/sanitize"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "Show version and exit")
	flag.BoolVar(showVersion, "v", false, "Show version and exit (shorthand)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("android-studio-llm-proxy version %s\n", version)
		os.Exit(0)
	}

	// Load configuration.
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	if cfg.Created {
		slog.Info("created default config file", "path", cfg.Path)
	}

	// Override config with environment variables.
	if port := os.Getenv("PORT"); port != "" {
		cfg.Port = port
	} else if cfg.Port == "" {
		cfg.Port = "9999"
	}

	if upstream := os.Getenv("UPSTREAM_URL"); upstream != "" {
		cfg.UpstreamURL = upstream
	} else if cfg.UpstreamURL == "" {
		cfg.UpstreamURL = "https://opencode.ai/zen/go/v1"
	}

	if v := os.Getenv("CACHE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.CacheTTL = d
		}
	} else if cfg.CacheTTL == 0 {
		cfg.CacheTTL = time.Hour
	}

	if v := os.Getenv("CACHE_MAX_ENTRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CacheMaxEntries = n
		}
	} else if cfg.CacheMaxEntries == 0 {
		cfg.CacheMaxEntries = 1000
	}

	if v := os.Getenv("MAX_CONTEXT_MESSAGES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxContextMessages = n
		}
	}
	if cfg.MaxContextMessages == 0 {
		cfg.MaxContextMessages = 100
	}

	if v := os.Getenv("MAX_CONTEXT_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxContextTokens = n
		}
	}
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = 65536 // 64K, safe for most models
	}

	if v := os.Getenv("MAX_COMPLETION_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxCompletionTokens = n
		}
	}

	logLevel := slog.LevelInfo
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		switch strings.ToLower(v) {
		case "debug":
			logLevel = slog.LevelDebug
		case "info":
			logLevel = slog.LevelInfo
		case "warn", "warning":
			logLevel = slog.LevelWarn
		case "error":
			logLevel = slog.LevelError
		}
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})))

	reasoningCache := cache.NewMemoryCache(cfg.CacheTTL, cfg.CacheMaxEntries)
	fwd := forward.New(cfg.UpstreamURL, reasoningCache)

	slog.Info("config loaded",
		"path", cfg.Path,
		"upstream", cfg.UpstreamURL,
		"port", cfg.Port,
		"cache_ttl", cfg.CacheTTL,
		"cache_max", cfg.CacheMaxEntries,
		"max_context_messages", cfg.MaxContextMessages,
		"max_context_tokens", cfg.MaxContextTokens,
		"max_completion_tokens", cfg.MaxCompletionTokens,
		"model_overrides", len(cfg.Models),
	)

	mux := http.NewServeMux()

	// Health check endpoint.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Models endpoints — proxy as-is.
	mux.HandleFunc("GET /v1/models", withLogging(func(w http.ResponseWriter, r *http.Request) {
		fwd.ProxyGet(w, r, forward.ModelsPath)
	}))
	mux.HandleFunc("GET /models", withLogging(func(w http.ResponseWriter, r *http.Request) {
		fwd.ProxyGet(w, r, forward.ModelsPath)
	}))

	// Chat completions — sanitize then proxy.
	mux.HandleFunc("POST /v1/chat/completions", withLogging(func(w http.ResponseWriter, r *http.Request) {
		handleChat(w, r, fwd, reasoningCache, cfg)
	}))
	mux.HandleFunc("POST /chat/completions", withLogging(func(w http.ResponseWriter, r *http.Request) {
		handleChat(w, r, fwd, reasoningCache, cfg)
	}))

	addr := ":" + cfg.Port
	slog.Info("starting proxy", "version", version, "addr", addr, "upstream", cfg.UpstreamURL)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

const maxRequestBodySize = 10 << 20 // 10 MiB

func handleChat(w http.ResponseWriter, r *http.Request, fwd *forward.Forwarder, c cache.ReasoningCache, cfg *config.Config) {
	limited := http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	defer limited.Close()

	res, err := sanitize.Sanitize(limited, c, cfg)
	if err != nil {
		slog.Warn("sanitize failed", "error", err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	slog.Debug("sanitized request",
		"body", truncate(string(res.Body), 2000),
		"stream", res.IsStream,
	)

	fwd.Proxy(w, r, forward.ChatPath, res.Body, res.IsStream)
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// withLogging wraps a handler to log request duration and status.
func withLogging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// Extract model from request body for chat endpoints.
		var model string
		if r.Method == "POST" && strings.Contains(r.URL.Path, "chat/completions") {
			if body, err := io.ReadAll(r.Body); err == nil {
				var reqBody map[string]any
				if err := json.Unmarshal(body, &reqBody); err == nil {
					if m, ok := reqBody["model"].(string); ok {
						model = strings.TrimPrefix(m, "OpenAIAPI/models/")
					}
				}
				// Restore the body for the handler to read.
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
		}

		next(rw, r)
		duration := time.Since(start)

		if model != "" {
			slog.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"model", model,
				"status", rw.statusCode,
				"duration_ms", float64(duration.Microseconds())/1000.0,
			)
		} else {
			slog.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.statusCode,
				"duration_ms", float64(duration.Microseconds())/1000.0,
			)
		}
	}
}

// truncate returns s truncated to maxLen characters plus "..." suffix.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
