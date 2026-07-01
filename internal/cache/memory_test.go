package cache_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/yohanesgre/android-studio-llm-proxy/internal/cache"
)

func TestStoreAndFind(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	msg := cache.Message{
		Role:             "assistant",
		Content:          "hello world",
		ReasoningContent: "thinking about hello",
	}
	c.Store(msg)

	got, ok := c.Find("hello world", nil)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got != "thinking about hello" {
		t.Errorf("got %q, want %q", got, "thinking about hello")
	}
}

func TestStoreEmptyReasoningSkipped(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	msg := cache.Message{
		Role:             "assistant",
		Content:          "hello",
		ReasoningContent: "",
	}
	c.Store(msg)

	_, ok := c.Find("hello", nil)
	if ok {
		t.Error("expected cache miss for empty reasoning")
	}
}

func TestFindMiss(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	_, ok := c.Find("nonexistent", nil)
	if ok {
		t.Error("expected cache miss")
	}
}

func TestFindWithToolCalls(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	tc := []cache.ToolCall{{ID: "tc1", Type: "function"}}
	tc[0].Function.Name = "get_weather"
	tc[0].Function.Arguments = `{"city":"SF"}`

	msg := cache.Message{
		Role:             "assistant",
		Content:          "",
		ToolCalls:        tc,
		ReasoningContent: "deciding to call weather",
	}
	c.Store(msg)

	got, ok := c.Find("", tc)
	if !ok {
		t.Fatal("expected cache hit with tool calls")
	}
	if got != "deciding to call weather" {
		t.Errorf("got %q, want %q", got, "deciding to call weather")
	}

	// Different tool calls should miss.
	tc2 := []cache.ToolCall{{ID: "tc2", Type: "function"}}
	_, ok2 := c.Find("", tc2)
	if ok2 {
		t.Error("expected cache miss for different tool calls")
	}
}

func TestTTLEviction(t *testing.T) {
	c := cache.NewMemoryCache(50*time.Millisecond, 100)
	msg := cache.Message{
		Role:             "assistant",
		Content:          "ttl test",
		ReasoningContent: "reasoning",
	}
	c.Store(msg)

	// Should hit immediately.
	if _, ok := c.Find("ttl test", nil); !ok {
		t.Fatal("expected hit before TTL")
	}

	time.Sleep(100 * time.Millisecond)

	// Should miss after TTL.
	if _, ok := c.Find("ttl test", nil); ok {
		t.Error("expected miss after TTL")
	}
}

func TestMaxEntriesEviction(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 3)

	for i := 0; i < 5; i++ {
		c.Store(cache.Message{
			Role:             "assistant",
			Content:          fmt.Sprintf("msg-%d", i),
			ReasoningContent: fmt.Sprintf("reasoning-%d", i),
		})
	}

	// First two should be evicted.
	if _, ok := c.Find("msg-0", nil); ok {
		t.Error("msg-0 should be evicted")
	}
	if _, ok := c.Find("msg-1", nil); ok {
		t.Error("msg-1 should be evicted")
	}

	// Last three should be present.
	for i := 2; i < 5; i++ {
		if _, ok := c.Find(fmt.Sprintf("msg-%d", i), nil); !ok {
			t.Errorf("msg-%d should be present", i)
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 1000)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			c.Store(cache.Message{
				Role:             "assistant",
				Content:          fmt.Sprintf("concurrent-%d", i),
				ReasoningContent: fmt.Sprintf("reasoning-%d", i),
			})
		}(i)
		go func(i int) {
			defer wg.Done()
			c.Find(fmt.Sprintf("concurrent-%d", i), nil)
		}(i)
	}

	wg.Wait()
}

func TestFallbackMatchByContent(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	tc := []cache.ToolCall{{ID: "tc1", Type: "function"}}
	tc[0].Function.Name = "get_weather"
	tc[0].Function.Arguments = `{"city":"SF"}`

	msg := cache.Message{
		Role:             "assistant",
		Content:          "the answer is 42",
		ToolCalls:        tc,
		ReasoningContent: "thinking about weather",
	}
	c.Store(msg)

	// Find with same content but no tool calls should hit via content match.
	got, ok := c.Find("the answer is 42", nil)
	if !ok {
		t.Fatal("expected cache hit via content match")
	}
	if got != "thinking about weather" {
		t.Errorf("got %q, want %q", got, "thinking about weather")
	}
}

func TestFallbackMatchByToolCallID(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	tc := []cache.ToolCall{{ID: "call_abc123", Type: "function"}}
	tc[0].Function.Name = "get_weather"
	tc[0].Function.Arguments = `{"city":"SF"}`

	msg := cache.Message{
		Role:             "assistant",
		Content:          "",
		ToolCalls:        tc,
		ReasoningContent: "deciding to call weather",
	}
	c.Store(msg)

	// Find with same tool call ID but different arguments should hit via tool-call-ID match.
	tc2 := []cache.ToolCall{{ID: "call_abc123", Type: "function"}}
	tc2[0].Function.Name = "get_weather"
	tc2[0].Function.Arguments = `{"city":"NYC"}`

	got, ok := c.Find("", tc2)
	if !ok {
		t.Fatal("expected cache hit via tool-call-ID match")
	}
	if got != "deciding to call weather" {
		t.Errorf("got %q, want %q", got, "deciding to call weather")
	}
}

func TestFallbackMatchFuzzyContent(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	msg := cache.Message{
		Role:             "assistant",
		Content:          "the  answer   is    42",
		ReasoningContent: "thinking step by step",
	}
	c.Store(msg)

	// Find with normalized content (extra spaces trimmed) should hit via fuzzy match.
	got, ok := c.Find("the answer is 42", nil)
	if !ok {
		t.Fatal("expected cache hit via fuzzy content match")
	}
	if got != "thinking step by step" {
		t.Errorf("got %q, want %q", got, "thinking step by step")
	}
}

func TestFallbackMatchWithLeadingTrailingSpaces(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	msg := cache.Message{
		Role:             "assistant",
		Content:          "  hello world  ",
		ReasoningContent: "greeting reasoning",
	}
	c.Store(msg)

	// Find with trimmed content should hit via fuzzy match.
	got, ok := c.Find("hello world", nil)
	if !ok {
		t.Fatal("expected cache hit via fuzzy content match with trimmed spaces")
	}
	if got != "greeting reasoning" {
		t.Errorf("got %q, want %q", got, "greeting reasoning")
	}
}
