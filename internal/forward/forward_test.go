package forward_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yohanesgre/android-studio-llm-proxy/internal/cache"
	"github.com/yohanesgre/android-studio-llm-proxy/internal/forward"
)

func TestNonStreamCachesReasoning(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "the answer is 42",
					"reasoning_content": "let me think..."
				}
			}]
		}`))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, c)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), false)

	// Verify cache has the reasoning.
	got, ok := c.Find("the answer is 42", nil)
	if !ok {
		t.Fatal("expected reasoning to be cached")
	}
	if got != "let me think..." {
		t.Errorf("cached reasoning = %q, want %q", got, "let me think...")
	}
}

func TestNonStreamNoReasoningNotCached(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "hello"
				}
			}]
		}`))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, c)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), false)

	_, ok := c.Find("hello", nil)
	if ok {
		t.Error("should not cache message without reasoning_content")
	}
}

func TestStreamCachesReasoning(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)

	sseData := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"choices":[{"index":0,"delta":{"reasoning_content":"thinking "}}]}

data: {"choices":[{"index":0,"delta":{"reasoning_content":"hard"}}]}

data: {"choices":[{"index":0,"delta":{"content":"the answer"}}]}

data: [DONE]

`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseData))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, c)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), true)

	// Verify the response was forwarded.
	body := w.Body.String()
	if !strings.Contains(body, "the answer") {
		t.Error("response should contain streamed content")
	}

	// Verify cache has the reasoning.
	got, ok := c.Find("the answer", nil)
	if !ok {
		t.Fatal("expected reasoning to be cached from stream")
	}
	if got != "thinking hard" {
		t.Errorf("cached reasoning = %q, want %q", got, "thinking hard")
	}
}

func TestStreamWithToolCalls(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)

	sseData := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}

data: {"choices":[{"index":0,"delta":{"reasoning_content":"need weather"}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tc1","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"SF\"}"}}]}}]}

data: [DONE]

`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseData))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, c)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), true)

	// Verify cache has the reasoning with tool calls.
	tc := []cache.ToolCall{{ID: "tc1", Type: "function"}}
	tc[0].Function.Name = "get_weather"
	tc[0].Function.Arguments = `{"city":"SF"}`

	got, ok := c.Find("", tc)
	if !ok {
		t.Fatal("expected reasoning to be cached with tool calls")
	}
	if got != "need weather" {
		t.Errorf("cached reasoning = %q, want %q", got, "need weather")
	}
}

func TestMultipleChoicesCached(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"choices": [
				{"index":0,"message":{"role":"assistant","content":"choice 0","reasoning_content":"reasoning 0"}},
				{"index":1,"message":{"role":"assistant","content":"choice 1","reasoning_content":"reasoning 1"}}
			]
		}`))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, c)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), false)

	got0, ok0 := c.Find("choice 0", nil)
	if !ok0 || got0 != "reasoning 0" {
		t.Errorf("choice 0: got %q, ok=%v, want %q", got0, ok0, "reasoning 0")
	}

	got1, ok1 := c.Find("choice 1", nil)
	if !ok1 || got1 != "reasoning 1" {
		t.Errorf("choice 1: got %q, ok=%v, want %q", got1, ok1, "reasoning 1")
	}
}

func TestNilCacheDoesNotCrash(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi","reasoning_content":"thinking"}}]}`))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, nil)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), false)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestResponseWrittenCorrectly(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, c)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), false)

	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	expected := `{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`
	if w.Body.String() != expected {
		t.Errorf("body = %q, want %q", w.Body.String(), expected)
	}
}

func TestProxy429LimitError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"type":"error","error":{"type":"RateLimitError","message":"Rate limit exceeded."}}`))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, nil)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), false)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
	if ra := w.Header().Get("Retry-After"); ra != "42" {
		t.Errorf("retry-after = %q, want 42", ra)
	}

	var got struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Error.Code != "rate_limit_exceeded" {
		t.Errorf("code = %q, want rate_limit_exceeded", got.Error.Code)
	}
	if got.Error.Type != "rate_limit_error" {
		t.Errorf("type = %q, want rate_limit_error", got.Error.Type)
	}
	if !strings.Contains(got.Error.Message, "Rate limit exceeded") {
		t.Errorf("message = %q, want to contain 'Rate limit exceeded'", got.Error.Message)
	}
}

func TestProxy429GoUsageLimitError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"type":"error","error":{"type":"GoUsageLimitError","message":"go usage limit reached"}}`))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, nil)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), false)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}

	var got struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Error.Code != "go_usage_limit_exceeded" {
		t.Errorf("code = %q, want go_usage_limit_exceeded", got.Error.Code)
	}
}

func TestProxy429UnrecognizedBody(t *testing.T) {
	original := `{"message":"something else","code":"weird"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(original))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, nil)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), false)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
	if w.Body.String() != original {
		t.Errorf("body = %q, want unchanged %q", w.Body.String(), original)
	}
}

func TestProxy429StreamMode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"type":"error","error":{"type":"FreeUsageLimitError","message":"free tier exhausted"}}`))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, nil)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	// isStream=true, but 429 must still be transformed (not streamed).
	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), true)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}

	var got struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got.Error.Code != "free_usage_limit_exceeded" {
		t.Errorf("code = %q, want free_usage_limit_exceeded", got.Error.Code)
	}
	if !strings.Contains(got.Error.Message, "free tier exhausted") {
		t.Errorf("message = %q, want to contain 'free tier exhausted'", got.Error.Message)
	}
}

// writeRecorder wraps an http.ResponseWriter and records each Write call so
// tests can verify that the proxy only writes complete SSE events.
type writeRecorder struct {
	http.ResponseWriter
	writes [][]byte
}

func (w *writeRecorder) Write(b []byte) (int, error) {
	cp := make([]byte, len(b))
	copy(cp, b)
	w.writes = append(w.writes, cp)
	return w.ResponseWriter.Write(b)
}

func (w *writeRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func TestStreamForwardsCompleteEventsOnly(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)

	sseEvent := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello world\"}}]}\n\n"

	// Upstream writes the event byte-by-byte with flushes to simulate
	// worst-case fragmented network delivery.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < len(sseEvent); i++ {
			w.Write([]byte{sseEvent[i]})
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, c)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	wr := &writeRecorder{ResponseWriter: rec}

	fwd.Proxy(wr, req, "/chat/completions", []byte(`{}`), true)

	body := rec.Body.String()
	if !strings.Contains(body, "hello world") {
		t.Fatal("expected streamed content in response")
	}

	// Each Write call to the client must be a complete SSE event (ending with \n\n).
	// This is the key assertion: the proxy must not forward partial events.
	for i, w := range wr.writes {
		if !strings.HasSuffix(string(w), "\n\n") {
			t.Errorf("write %d is not a complete SSE event (missing trailing blank line): %q", i, w)
		}
	}

	// Additionally verify every data: line contains valid JSON.
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var js json.RawMessage
		if err := json.Unmarshal([]byte(payload), &js); err != nil {
			t.Errorf("data line is not valid JSON: %q, err: %v", line, err)
		}
	}
}

func TestStreamMultipleEvents(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)

	sseData := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking \"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"hard\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"the answer\"}}]}\n\n" +
		"data: [DONE]\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseData))
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, c)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	wr := &writeRecorder{ResponseWriter: rec}

	fwd.Proxy(wr, req, "/chat/completions", []byte(`{}`), true)

	body := rec.Body.String()

	// Content must be forwarded (as a coalesced synthetic event).
	if !strings.Contains(body, "the answer") {
		t.Errorf("response missing %q", "the answer")
	}

	// [DONE] must be forwarded.
	if !strings.Contains(body, "[DONE]") {
		t.Error("response missing [DONE]")
	}

	// reasoning_content events are NOT forwarded to the client.
	if strings.Contains(body, "thinking ") {
		t.Error("reasoning_content should not be forwarded to client")
	}

	// Each write must be a complete event.
	for i, w := range wr.writes {
		if !strings.HasSuffix(string(w), "\n\n") {
			t.Errorf("write %d is not a complete SSE event: %q", i, w)
		}
	}

	// Caching must still work.
	got, ok := c.Find("the answer", nil)
	if !ok {
		t.Fatal("expected reasoning to be cached from stream")
	}
	if got != "thinking hard" {
		t.Errorf("cached reasoning = %q, want %q", got, "thinking hard")
	}
}

func TestStreamPartialEventAtEOF(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)

	// Partial event: no trailing blank line, simulating an upstream that
	// closes the connection mid-stream.
	partialEvent := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(partialEvent))
		// No blank line — connection closes immediately.
	}))
	defer upstream.Close()

	fwd := forward.New(upstream.URL, c)

	req := httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	fwd.Proxy(w, req, "/chat/completions", []byte(`{}`), true)

	body := w.Body.String()
	if !strings.Contains(body, "partial") {
		t.Fatal("expected partial event to be forwarded")
	}
	// The proxy must append \n\n so the event is properly terminated.
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("expected trailing \\n\\n, got body ending %q", body[len(body)-min(10, len(body)):])
	}

	// Cache must not crash on partial data.
	// (No reasoning_content in this event, so nothing should be cached.)
}
