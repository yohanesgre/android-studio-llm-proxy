package forward_test

import (
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
