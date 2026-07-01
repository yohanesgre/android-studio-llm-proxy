package forward

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yohanesgre/android-studio-llm-proxy/internal/cache"
)

// Forwarder proxies requests to the upstream LLM API.
type Forwarder struct {
	upstreamURL string
	client      *http.Client
	cache       cache.ReasoningCache
}

// hopByHopHeaders lists headers that should not be forwarded from upstream.
// See RFC 2616 §13.5.1. Set-Cookie is also stripped to avoid leaking upstream
// cookies to the local Android Studio client.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Set-Cookie":          true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func isHopByHopHeader(name string) bool {
	return hopByHopHeaders[http.CanonicalHeaderKey(name)]
}

// New creates a Forwarder that sends requests to the given upstream base URL.
func New(upstreamURL string, c cache.ReasoningCache) *Forwarder {
	return &Forwarder{
		upstreamURL: strings.TrimRight(upstreamURL, "/"),
		client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		cache: c,
	}
}

// opencodeError represents the error shape returned by OpenCode Go.
type opencodeError struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// openAIError is the OpenAI-compatible error shape we emit to the client.
type openAIError struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Code    string  `json:"code"`
		Param   *string `json:"param"`
	} `json:"error"`
}

// mapOpenCodeErrorType maps an OpenCode error type to an OpenAI error code.
func mapOpenCodeErrorType(t string) string {
	switch t {
	case "RateLimitError":
		return "rate_limit_exceeded"
	case "FreeUsageLimitError":
		return "free_usage_limit_exceeded"
	case "GoUsageLimitError":
		return "go_usage_limit_exceeded"
	case "BlackUsageLimitError":
		return "black_usage_limit_exceeded"
	default:
		return "rate_limit_exceeded"
	}
}

// transformLimitError attempts to parse body as an OpenCode limit error and
// returns the OpenAI-compatible JSON. If the body is not a recognised OpenCode
// error, it returns the original body unchanged and ok=false.
func transformLimitError(body []byte) (out []byte, ok bool) {
	var oe opencodeError
	if err := json.Unmarshal(body, &oe); err != nil {
		return body, false
	}
	if oe.Type != "error" || oe.Error.Type == "" {
		return body, false
	}

	var ae openAIError
	ae.Error.Message = oe.Error.Message
	ae.Error.Type = "rate_limit_error"
	ae.Error.Code = mapOpenCodeErrorType(oe.Error.Type)
	ae.Error.Param = nil

	encoded, err := json.Marshal(ae)
	if err != nil {
		return body, false
	}
	return encoded, true
}

// Proxy sends the request to the upstream path and writes the response to w.
// For streaming responses, data is flushed chunk-by-chunk.
func (f *Forwarder) Proxy(w http.ResponseWriter, r *http.Request, path string, body []byte, isStream bool) {
	upstreamURL := f.upstreamURL + path

	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		slog.Error("forward: create request", "error", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	// Pass through Authorization header.
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	if isStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}

	start := time.Now()
	resp, err := f.client.Do(req)
	if err != nil {
		slog.Error("forward: upstream request", "error", err, "url", upstreamURL)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	firstByte := time.Since(start)

	// Copy response headers, stripping hop-by-hop and sensitive headers.
	for k, vv := range resp.Header {
		if isHopByHopHeader(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Handle 429 limit errors from OpenCode Go: transform to OpenAI shape and
	// return early (do not stream or cache).
	if resp.StatusCode == http.StatusTooManyRequests {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			slog.Error("forward: read 429 body", "error", err)
			return
		}
		out, transformed := transformLimitError(data)
		if _, werr := w.Write(out); werr != nil {
			slog.Error("forward: write 429 response", "error", werr)
		}
		total := time.Since(start)
		if transformed {
			slog.Warn("upstream rate limited",
				"url", upstreamURL,
				"status", resp.StatusCode,
				"first_byte_ms", float64(firstByte.Microseconds())/1000.0,
				"total_ms", float64(total.Microseconds())/1000.0,
			)
		} else {
			slog.Info("upstream",
				"url", upstreamURL,
				"status", resp.StatusCode,
				"first_byte_ms", float64(firstByte.Microseconds())/1000.0,
				"total_ms", float64(total.Microseconds())/1000.0,
			)
		}
		return
	}

	if isStream {
		f.streamResponse(w, resp.Body)
	} else {
		f.nonStreamResponse(w, resp.Body)
	}

	total := time.Since(start)
	slog.Info("upstream",
		"url", upstreamURL,
		"status", resp.StatusCode,
		"first_byte_ms", float64(firstByte.Microseconds())/1000.0,
		"total_ms", float64(total.Microseconds())/1000.0,
	)
}

// nonStreamResponse reads the full response body, caches reasoning_content, and writes to w.
func (f *Forwarder) nonStreamResponse(w http.ResponseWriter, body io.Reader) {
	data, err := io.ReadAll(body)
	if err != nil {
		slog.Error("forward: read response", "error", err)
		return
	}

	if f.cache != nil {
		f.cacheNonStreamResponse(data)
	}

	if _, err := w.Write(data); err != nil {
		slog.Error("forward: write response", "error", err)
	}
}

// cacheNonStreamResponse parses a non-streaming response and caches reasoning_content.
func (f *Forwarder) cacheNonStreamResponse(data []byte) {
	var resp struct {
		Choices []struct {
			Message struct {
				Role             string     `json:"role"`
				Content          string     `json:"content"`
				ToolCalls        []toolCall `json:"tool_calls"`
				ReasoningContent string     `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		slog.Debug("forward: parse non-stream response for cache", "error", err)
		return
	}
	for _, choice := range resp.Choices {
		msg := choice.Message
		if msg.ReasoningContent == "" {
			continue
		}
		f.cache.Store(cache.Message{
			Role:             msg.Role,
			Content:          msg.Content,
			ToolCalls:        convertToolCalls(msg.ToolCalls),
			ReasoningContent: msg.ReasoningContent,
		})
	}
}

// streamResponse reads from upstream SSE and flushes to the client.
func (f *Forwarder) streamResponse(w http.ResponseWriter, body io.Reader) {
	flusher, canFlush := w.(http.Flusher)

	// Use a pool of buffers for streaming.
	buf := streamBufPool.Get().(*[]byte)
	defer streamBufPool.Put(buf)

	// Accumulate SSE data for caching reasoning_content.
	var sseAccum strings.Builder

	for {
		n, err := body.Read(*buf)
		if n > 0 {
			chunk := (*buf)[:n]
			if _, werr := w.Write(chunk); werr != nil {
				slog.Error("forward: write stream chunk", "error", werr)
				return
			}
			if canFlush {
				flusher.Flush()
			}
			if f.cache != nil {
				sseAccum.Write(chunk)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			slog.Error("forward: read stream", "error", err)
			return
		}
	}

	if f.cache != nil {
		f.cacheStreamResponse(sseAccum.String())
	}
}

// cacheStreamResponse parses accumulated SSE data and caches reasoning_content.
func (f *Forwarder) cacheStreamResponse(sseData string) {
	// Accumulate deltas per choice index.
	type toolCallAccum struct {
		id   string
		typ  string
		name strings.Builder
		args strings.Builder
	}
	type deltaAccum struct {
		role             string
		content          strings.Builder
		reasoningContent strings.Builder
		toolCalls        map[int]*toolCallAccum
	}

	accums := make(map[int]*deltaAccum)

	scanner := bufio.NewScanner(strings.NewReader(sseData))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Role             string     `json:"role"`
					Content          string     `json:"content"`
					ReasoningContent string     `json:"reasoning_content"`
					ToolCalls        []toolCall `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			acc, ok := accums[choice.Index]
			if !ok {
				acc = &deltaAccum{toolCalls: make(map[int]*toolCallAccum)}
				accums[choice.Index] = acc
			}
			d := choice.Delta
			if d.Role != "" {
				acc.role = d.Role
			}
			acc.content.WriteString(d.Content)
			acc.reasoningContent.WriteString(d.ReasoningContent)
			for _, tc := range d.ToolCalls {
				idx := tc.Index
				tca, ok := acc.toolCalls[idx]
				if !ok {
					tca = &toolCallAccum{}
					acc.toolCalls[idx] = tca
				}
				if tc.ID != "" {
					tca.id = tc.ID
				}
				if tc.Type != "" {
					tca.typ = tc.Type
				}
				if tc.Function.Name != "" {
					tca.name.WriteString(tc.Function.Name)
				}
				tca.args.WriteString(tc.Function.Arguments)
			}
		}
	}

	// Store completed messages in cache.
	for _, acc := range accums {
		reasoning := acc.reasoningContent.String()
		if reasoning == "" {
			continue
		}
		var toolCalls []cache.ToolCall
		// Sort by tool-call index to keep deterministic order.
		idxs := make([]int, 0, len(acc.toolCalls))
		for idx := range acc.toolCalls {
			idxs = append(idxs, idx)
		}
		sort.Ints(idxs)
		for _, idx := range idxs {
			tca := acc.toolCalls[idx]
			var tc cache.ToolCall
			tc.ID = tca.id
			tc.Type = tca.typ
			tc.Function.Name = tca.name.String()
			tc.Function.Arguments = tca.args.String()
			toolCalls = append(toolCalls, tc)
		}
		f.cache.Store(cache.Message{
			Role:             acc.role,
			Content:          acc.content.String(),
			ToolCalls:        toolCalls,
			ReasoningContent: reasoning,
		})
	}
}

type toolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func convertToolCalls(tcs []toolCall) []cache.ToolCall {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]cache.ToolCall, len(tcs))
	for i, tc := range tcs {
		out[i].ID = tc.ID
		out[i].Type = tc.Type
		out[i].Function.Name = tc.Function.Name
		out[i].Function.Arguments = tc.Function.Arguments
	}
	return out
}

var streamBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 4096)
		return &b
	},
}

// ProxyGet forwards a GET request (no body) to the upstream path.
func (f *Forwarder) ProxyGet(w http.ResponseWriter, r *http.Request, path string) {
	f.Proxy(w, r, path, nil, false)
}

// ModelsPath is the upstream path for the models endpoint.
const ModelsPath = "/models"

// ChatPath is the upstream path for chat completions.
const ChatPath = "/chat/completions"
