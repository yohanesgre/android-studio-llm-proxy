package sanitize_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yohanesgre/android-studio-llm-proxy/internal/cache"
	"github.com/yohanesgre/android-studio-llm-proxy/internal/config"
	"github.com/yohanesgre/android-studio-llm-proxy/internal/sanitize"
)

func mustSanitize(t *testing.T, body string) map[string]any {
	t.Helper()
	res, err := sanitize.Sanitize(strings.NewReader(body), nil, &config.Config{MaxContextMessages: 1000})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(res.Body, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	return req
}

func mustSanitizeWithCache(t *testing.T, body string, c cache.ReasoningCache) map[string]any {
	t.Helper()
	res, err := sanitize.Sanitize(strings.NewReader(body), c, &config.Config{MaxContextMessages: 1000})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(res.Body, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	return req
}

func mustSanitizeWithOverrides(t *testing.T, body string, overrides map[string]map[string]any) map[string]any {
	t.Helper()
	res, err := sanitize.Sanitize(strings.NewReader(body), nil, &config.Config{Models: overrides, MaxContextMessages: 1000})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(res.Body, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	return req
}

func TestStripModelPrefix(t *testing.T) {
	req := mustSanitize(t, `{"model":"OpenAIAPI/models/kimi-k2.7-code","messages":[]}`)
	if req["model"] != "kimi-k2.7-code" {
		t.Errorf("model = %v, want kimi-k2.7-code", req["model"])
	}
}

func TestStripModelPrefixNoPrefix(t *testing.T) {
	req := mustSanitize(t, `{"model":"gpt-4","messages":[]}`)
	if req["model"] != "gpt-4" {
		t.Errorf("model = %v, want gpt-4", req["model"])
	}
}

func TestDeveloperRoleMappedToSystem(t *testing.T) {
	body := `{"model":"gpt-4","messages":[{"role":"developer","content":"hi"},{"role":"user","content":"yo"}]}`
	req := mustSanitize(t, body)
	msgs := req["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("role = %v, want system", first["role"])
	}
	second := msgs[1].(map[string]any)
	if second["role"] != "user" {
		t.Errorf("role = %v, want user (unchanged)", second["role"])
	}
}

func TestKimiK27StripsSamplingParams(t *testing.T) {
	body := `{"model":"kimi-k2.7-code","messages":[],"temperature":0.7,"top_p":0.9,"presence_penalty":0.1,"frequency_penalty":0.2,"n":2}`
	req := mustSanitize(t, body)
	for _, key := range []string{"temperature", "top_p", "presence_penalty", "frequency_penalty", "n"} {
		if _, ok := req[key]; ok {
			t.Errorf("key %q should be stripped for kimi-k2.7", key)
		}
	}
}

func TestKimiK26StripsSamplingParams(t *testing.T) {
	body := `{"model":"kimi-k2.6","messages":[],"temperature":0.5,"n":3}`
	req := mustSanitize(t, body)
	if _, ok := req["temperature"]; ok {
		t.Error("temperature should be stripped for kimi-k2.6")
	}
	if _, ok := req["n"]; ok {
		t.Error("n should be stripped for kimi-k2.6")
	}
}

func TestToolChoiceNormalizedForKimi(t *testing.T) {
	body := `{"model":"kimi-k2.7","messages":[],"tool_choice":"required"}`
	req := mustSanitize(t, body)
	if req["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", req["tool_choice"])
	}
}

func TestToolChoiceKeptAutoForKimi(t *testing.T) {
	body := `{"model":"kimi-k2.7","messages":[],"tool_choice":"auto"}`
	req := mustSanitize(t, body)
	if req["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", req["tool_choice"])
	}
}

func TestToolChoiceKeptNoneForKimi(t *testing.T) {
	body := `{"model":"kimi-k2.7","messages":[],"tool_choice":"none"}`
	req := mustSanitize(t, body)
	if req["tool_choice"] != "none" {
		t.Errorf("tool_choice = %v, want none", req["tool_choice"])
	}
}

func TestToolChoiceObjectNormalizedForDeepSeekV4(t *testing.T) {
	body := `{"model":"deepseek-v4-pro","messages":[],"tool_choice":{"type":"function","function":{"name":"foo"}}}`
	req := mustSanitize(t, body)
	if req["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", req["tool_choice"])
	}
}

func TestDeepSeekReasonerStripsReasoningContent(t *testing.T) {
	body := `{"model":"deepseek-reasoner","messages":[{"role":"assistant","content":"hi","reasoning_content":"thinking..."}]}`
	req := mustSanitize(t, body)
	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if _, ok := msg["reasoning_content"]; ok {
		t.Error("reasoning_content should be stripped from input messages")
	}
}

func TestQwen37ParsesToolArguments(t *testing.T) {
	body := `{
		"model":"qwen3.7-plus",
		"messages":[],
		"tools":[
			{"type":"function","function":{"name":"foo","arguments":"{\"x\":1}"}},
			{"type":"function","function":{"name":"bar","arguments":{"y":2}}}
		]
	}`
	req := mustSanitize(t, body)
	tools := req["tools"].([]any)

	first := tools[0].(map[string]any)
	fn1 := first["function"].(map[string]any)
	args1, ok := fn1["arguments"].(map[string]any)
	if !ok {
		t.Fatalf("arguments should be parsed to object, got %T", fn1["arguments"])
	}
	if args1["x"] != float64(1) {
		t.Errorf("x = %v, want 1", args1["x"])
	}

	second := tools[1].(map[string]any)
	fn2 := second["function"].(map[string]any)
	if _, ok := fn2["arguments"].(map[string]any); !ok {
		t.Errorf("arguments already an object should stay as-is, got %T", fn2["arguments"])
	}
}

func TestStreamFlagPreserved(t *testing.T) {
	res, err := sanitize.Sanitize(strings.NewReader(`{"model":"gpt-4","messages":[],"stream":true}`), nil, &config.Config{MaxContextMessages: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsStream {
		t.Error("expected IsStream = true")
	}

	res2, err := sanitize.Sanitize(strings.NewReader(`{"model":"gpt-4","messages":[]}`), nil, &config.Config{MaxContextMessages: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if res2.IsStream {
		t.Error("expected IsStream = false")
	}
}

func TestInvalidJSON(t *testing.T) {
	_, err := sanitize.Sanitize(strings.NewReader(`{not json`), nil, &config.Config{MaxContextMessages: 1000})
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestUnknownModelNoRulesApplied(t *testing.T) {
	body := `{"model":"gpt-4","messages":[],"temperature":0.5,"tool_choice":"required"}`
	req := mustSanitize(t, body)
	if req["temperature"] != 0.5 {
		t.Error("temperature should be preserved for unknown model")
	}
	if req["tool_choice"] != "required" {
		t.Error("tool_choice should be preserved for unknown model")
	}
}

func TestKimiHighspeedModel(t *testing.T) {
	body := `{"model":"kimi-k2.7-code-highspeed","messages":[],"temperature":0.9}`
	req := mustSanitize(t, body)
	if _, ok := req["temperature"]; ok {
		t.Error("temperature should be stripped for kimi-k2.7-code-highspeed")
	}
}

func TestReasoningInjectedFromCache(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	c.Store(cache.Message{
		Role:             "assistant",
		Content:          "the answer is 42",
		ReasoningContent: "let me think step by step...",
	})

	body := `{"model":"gpt-4","messages":[{"role":"assistant","content":"the answer is 42"}]}`
	req := mustSanitizeWithCache(t, body, c)
	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	rc, ok := msg["reasoning_content"].(string)
	if !ok || rc != "let me think step by step..." {
		t.Errorf("reasoning_content = %v, want %q", msg["reasoning_content"], "let me think step by step...")
	}
}

func TestReasoningNotOverwritten(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	c.Store(cache.Message{
		Role:             "assistant",
		Content:          "hello",
		ReasoningContent: "cached reasoning",
	})

	body := `{"model":"gpt-4","messages":[{"role":"assistant","content":"hello","reasoning_content":"existing reasoning"}]}`
	req := mustSanitizeWithCache(t, body, c)
	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	rc, _ := msg["reasoning_content"].(string)
	if rc != "existing reasoning" {
		t.Errorf("reasoning_content = %v, want %q (should not be overwritten)", rc, "existing reasoning")
	}
}

func TestReasoningNonAssistantIgnored(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	c.Store(cache.Message{
		Role:             "assistant",
		Content:          "hello",
		ReasoningContent: "cached reasoning",
	})

	body := `{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`
	req := mustSanitizeWithCache(t, body, c)
	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if _, ok := msg["reasoning_content"]; ok {
		t.Error("user message should not get reasoning_content injected")
	}
}

func TestReasoningCacheMissNoChange(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	// Cache is empty, so no hit.

	body := `{"model":"gpt-4","messages":[{"role":"assistant","content":"something not cached"}]}`
	req := mustSanitizeWithCache(t, body, c)
	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if _, ok := msg["reasoning_content"]; ok {
		t.Error("cache miss should not add reasoning_content")
	}
}

func TestReasoningWithToolCalls(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	tc := []cache.ToolCall{{ID: "tc1", Type: "function"}}
	tc[0].Function.Name = "get_weather"
	tc[0].Function.Arguments = `{"city":"SF"}`
	c.Store(cache.Message{
		Role:             "assistant",
		Content:          "",
		ToolCalls:        tc,
		ReasoningContent: "deciding to call weather",
	})

	body := `{"model":"gpt-4","messages":[{"role":"assistant","content":"","tool_calls":[{"id":"tc1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"SF\"}"}}]}]}`
	req := mustSanitizeWithCache(t, body, c)
	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	rc, ok := msg["reasoning_content"].(string)
	if !ok || rc != "deciding to call weather" {
		t.Errorf("reasoning_content = %v, want %q", msg["reasoning_content"], "deciding to call weather")
	}
}

func TestDeepSeekV4OverrideSetsThinkingAndReasoningEffort(t *testing.T) {
	overrides := map[string]map[string]any{
		"deepseek-v4-flash": {
			"thinking":         map[string]any{"type": "enabled"},
			"reasoning_effort": "high",
		},
	}

	body := `{"model":"deepseek-v4-flash","messages":[]}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	thinking, ok := req["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking to be set")
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", thinking["type"])
	}
	if req["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", req["reasoning_effort"])
	}
}

func TestDeepSeekV4DisabledThinkingPreservesToolChoice(t *testing.T) {
	overrides := map[string]map[string]any{
		"deepseek-v4-pro": {
			"thinking": map[string]any{"type": "disabled"},
		},
	}

	body := `{"model":"deepseek-v4-pro","messages":[],"tool_choice":"required"}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	if req["tool_choice"] != "required" {
		t.Errorf("tool_choice = %v, want required (should not be normalized when thinking disabled)", req["tool_choice"])
	}
}

func TestKimiK26DisabledThinkingPreservesSamplingParams(t *testing.T) {
	overrides := map[string]map[string]any{
		"kimi-k2.6": {
			"thinking": map[string]any{"type": "disabled"},
		},
	}

	body := `{"model":"kimi-k2.6","messages":[],"temperature":0.7,"top_p":0.9,"n":2}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	if req["temperature"] != 0.7 {
		t.Errorf("temperature = %v, want 0.7 (should be preserved when thinking disabled)", req["temperature"])
	}
	if req["top_p"] != 0.9 {
		t.Errorf("top_p = %v, want 0.9", req["top_p"])
	}
	if req["n"] != float64(2) {
		t.Errorf("n = %v, want 2", req["n"])
	}
}

func TestUnknownModelWithOverride(t *testing.T) {
	overrides := map[string]map[string]any{
		"gpt-4": {
			"custom_field": "custom_value",
			"temperature":  0.3,
		},
	}

	body := `{"model":"gpt-4","messages":[],"temperature":0.9}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	if req["custom_field"] != "custom_value" {
		t.Errorf("custom_field = %v, want custom_value", req["custom_field"])
	}
	if req["temperature"] != 0.3 {
		t.Errorf("temperature = %v, want 0.3 (should be overridden)", req["temperature"])
	}
}

func TestOverrideReplacesExistingField(t *testing.T) {
	overrides := map[string]map[string]any{
		"deepseek-v4-flash": {
			"temperature": 0.1,
		},
	}

	body := `{"model":"deepseek-v4-flash","messages":[],"temperature":0.9}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	if req["temperature"] != 0.1 {
		t.Errorf("temperature = %v, want 0.1 (should be overridden)", req["temperature"])
	}
}

func TestQwen37DisabledThinkingPreservesToolChoice(t *testing.T) {
	overrides := map[string]map[string]any{
		"qwen3.7-plus": {
			"enable_thinking": false,
		},
	}

	body := `{"model":"qwen3.7-plus","messages":[],"tool_choice":"required"}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	if req["tool_choice"] != "required" {
		t.Errorf("tool_choice = %v, want required (should not be normalized when enable_thinking=false)", req["tool_choice"])
	}
}

func TestDeepSeekV4MinimalPlaceholderOnCacheMiss(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	// Cache is empty, so no hit.

	body := `{"model":"deepseek-v4-flash","messages":[{"role":"assistant","content":"test message"}]}`
	req := mustSanitizeWithCache(t, body, c)
	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	rc, ok := msg["reasoning_content"].(string)
	if !ok {
		t.Fatal("expected minimal reasoning_content placeholder for DeepSeek")
	}
	if rc != "[...]" {
		t.Errorf("reasoning_content = %q, want placeholder '[...]'", rc)
	}
}

func TestDeepSeekReasonerMinimalPlaceholderOnCacheMiss(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	// Cache is empty, so no hit.

	body := `{"model":"deepseek-reasoner","messages":[{"role":"assistant","content":"test message"}]}`
	req := mustSanitizeWithCache(t, body, c)
	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	rc, ok := msg["reasoning_content"].(string)
	if !ok {
		t.Fatal("expected minimal reasoning_content placeholder for DeepSeek Reasoner")
	}
	if rc != "[...]" {
		t.Errorf("reasoning_content = %q, want placeholder '[...]'", rc)
	}
}

func TestKimiNoPlaceholder(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	// Cache is empty, so no hit.

	body := `{"model":"kimi-k2.7","messages":[{"role":"assistant","content":"test message"}]}`
	req := mustSanitizeWithCache(t, body, c)
	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if _, ok := msg["reasoning_content"]; ok {
		t.Error("Kimi should not get placeholder reasoning_content")
	}
}

func TestNormalizeThinkingDeepSeek(t *testing.T) {
	overrides := map[string]map[string]any{
		"deepseek-v4-flash": {
			"thinking": true,
			"reasoning_effort": "max",
		},
	}

	body := `{"model":"deepseek-v4-flash","messages":[]}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	thinking, ok := req["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %v, want map", req["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", thinking["type"])
	}
	if req["reasoning_effort"] != "max" {
		t.Errorf("reasoning_effort = %v, want max", req["reasoning_effort"])
	}
}

func TestNormalizeThinkingQwen(t *testing.T) {
	overrides := map[string]map[string]any{
		"qwen3.7-plus": {
			"thinking": false,
		},
	}

	body := `{"model":"qwen3.7-plus","messages":[]}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	if _, ok := req["thinking"]; ok {
		t.Error("thinking should be removed for Qwen")
	}
	if et, ok := req["enable_thinking"].(bool); !ok || et {
		t.Errorf("enable_thinking = %v, want false", req["enable_thinking"])
	}
}

func TestNormalizeThinkingKimi(t *testing.T) {
	overrides := map[string]map[string]any{
		"kimi-k2.6": {
			"thinking": true,
		},
	}

	body := `{"model":"kimi-k2.6","messages":[]}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	thinking, ok := req["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %v, want map", req["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", thinking["type"])
	}
}

func TestNormalizeThinkingProviderSpecificPassthrough(t *testing.T) {
	overrides := map[string]map[string]any{
		"deepseek-v4-flash": {
			"thinking": map[string]any{"type": "disabled"},
		},
	}

	body := `{"model":"deepseek-v4-flash","messages":[]}`
	req := mustSanitizeWithOverrides(t, body, overrides)

	thinking, ok := req["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %v, want map", req["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Errorf("thinking.type = %v, want disabled", thinking["type"])
	}
}

func TestStripImageURLsForNonVisionModel(t *testing.T) {
	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"https://example.com/img.png"}}]}]}`
	req := mustSanitize(t, body)

	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content len = %d, want 1", len(content))
	}
	part := content[0].(map[string]any)
	if part["type"] != "text" {
		t.Errorf("part type = %v, want text", part["type"])
	}
}

func TestKeepImageURLsForVisionModel(t *testing.T) {
	body := `{"model":"qwen3.7-plus","messages":[{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"https://example.com/img.png"}}]}]}`
	req := mustSanitize(t, body)

	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	content := msg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content len = %d, want 2", len(content))
	}
}

func TestStripImageURLsLeavesEmptyString(t *testing.T) {
	body := `{"model":"deepseek-v4-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/img.png"}}]}]}`
	req := mustSanitize(t, body)

	msgs := req["messages"].([]any)
	msg := msgs[0].(map[string]any)
	if msg["content"] != "" {
		t.Errorf("content = %v, want empty string", msg["content"])
	}
}

func TestSanitizeTrimsOldMessages(t *testing.T) {
	var msgs []string
	for i := 0; i < 120; i++ {
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"msg %d"}`, i))
	}
	body := fmt.Sprintf(`{"model":"gpt-4","messages":[%s]}`, strings.Join(msgs, ","))

	res, err := sanitize.Sanitize(strings.NewReader(body), nil, &config.Config{MaxContextMessages: 100})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(res.Body, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	got := req["messages"].([]any)
	if len(got) != 100 {
		t.Fatalf("got %d messages, want 100", len(got))
	}

	// The oldest 20 (msg 0..19) should be dropped; first remaining should be msg 20.
	first := got[0].(map[string]any)
	if first["content"] != "msg 20" {
		t.Errorf("first message content = %v, want msg 20", first["content"])
	}
	last := got[len(got)-1].(map[string]any)
	if last["content"] != "msg 119" {
		t.Errorf("last message content = %v, want msg 119", last["content"])
	}
}

func TestMaxCompletionTokensInjectedWhenConfigured(t *testing.T) {
	body := `{"model":"deepseek-v4-flash","messages":[]}`
	req := mustSanitizeWithOverrides(t, body, nil)
	// No default injection — only when user explicitly configures.
	if _, ok := req["max_completion_tokens"]; ok {
		t.Error("max_completion_tokens should not be injected without explicit config")
	}
}

func TestMaxCompletionTokensInjectedWithExplicitConfig(t *testing.T) {
	body := `{"model":"deepseek-v4-flash","messages":[]}`
	res, err := sanitize.Sanitize(strings.NewReader(body), nil, &config.Config{MaxCompletionTokens: 8192})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	json.Unmarshal(res.Body, &req)
	mct, ok := req["max_completion_tokens"]
	if !ok {
		t.Fatal("expected max_completion_tokens to be injected when explicitly configured")
	}
	if mct != float64(8192) {
		t.Errorf("max_completion_tokens = %v, want 8192", mct)
	}
}

func TestMaxCompletionTokensNotInjectedForNonDeepSeek(t *testing.T) {
	body := `{"model":"kimi-k2.7","messages":[]}`
	res, err := sanitize.Sanitize(strings.NewReader(body), nil, &config.Config{MaxCompletionTokens: 4096})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	json.Unmarshal(res.Body, &req)
	if _, ok := req["max_completion_tokens"]; ok {
		t.Error("max_completion_tokens should not be injected for non-DeepSeek models")
	}
}

func TestMaxCompletionTokensRespectsExisting(t *testing.T) {
	body := `{"model":"deepseek-v4-flash","messages":[],"max_completion_tokens":16384}`
	res, err := sanitize.Sanitize(strings.NewReader(body), nil, &config.Config{MaxCompletionTokens: 8192})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	json.Unmarshal(res.Body, &req)
	mct, ok := req["max_completion_tokens"]
	if !ok {
		t.Fatal("expected max_completion_tokens to be preserved")
	}
	// Client's own value takes precedence over config default.
	if mct != float64(16384) {
		t.Errorf("max_completion_tokens = %v, want 16384", mct)
	}
}

func TestSanitizeTrimsByTokens(t *testing.T) {
	var msgs []string
	for i := 0; i < 30; i++ {
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"%s"}`, strings.Repeat("x", 80)))
	}
	body := fmt.Sprintf(`{"model":"gpt-4","messages":[%s]}`, strings.Join(msgs, ","))

	// Each message: ~4 overhead + 20 tokens (80 chars/4) = ~24 tokens.
	// Budget 100 tokens should trim significantly.
	res, err := sanitize.Sanitize(strings.NewReader(body), nil, &config.Config{MaxContextTokens: 100})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(res.Body, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	got := req["messages"].([]any)
	if len(got) >= 30 {
		t.Errorf("expected messages to be trimmed by token budget, got %d", len(got))
	}
	if len(got) == 0 {
		t.Fatal("expected at least one message to remain")
	}
	// Most recent message should be preserved.
	last := got[len(got)-1].(map[string]any)
	if last["content"] != strings.Repeat("x", 80) {
		t.Errorf("last message content = %v, want msg 29 (80 x's)", last["content"])
	}
}

// TestPerModelMaxContextTokens verifies per-model override takes precedence over global.
func TestPerModelMaxContextTokens(t *testing.T) {
	var msgs []string
	for i := 0; i < 30; i++ {
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"%s"}`, strings.Repeat("x", 80)))
	}
	body := fmt.Sprintf(`{"model":"test-model-x","messages":[%s]}`, strings.Join(msgs, ","))

	cfg := &config.Config{
		MaxContextTokens: 100, // global: should keep ~4 messages
		Models: config.ModelOverrides{
			"test-model-x": {
				"max_context_tokens": float64(10), // per-model: should keep 0-1 messages
			},
		},
	}

	res, err := sanitize.Sanitize(strings.NewReader(body), nil, cfg)
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(res.Body, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	got := req["messages"].([]any)

	// With per-model max=10 tokens, and each message ~24 tokens, most should be dropped.
	// Even the most recent message (24 tokens) won't fit 10, but trimByTokens
	// keeps at least the last message.
	if len(got) >= 2 {
		t.Errorf("per-model override (10 tokens) should trim more aggressively than global (100), got %d messages", len(got))
	}
	if len(got) == 0 {
		t.Fatal("expected at least one message to remain")
	}
}

func TestSanitizePreservesSystemMessageWithTokenTrim(t *testing.T) {
	msgs := []string{`{"role":"system","content":"you are a helpful assistant"}`}
	for i := 0; i < 30; i++ {
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":"%s"}`, strings.Repeat("x", 80)))
	}
	body := fmt.Sprintf(`{"model":"gpt-4","messages":[%s]}`, strings.Join(msgs, ","))

	res, err := sanitize.Sanitize(strings.NewReader(body), nil, &config.Config{MaxContextTokens: 200})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(res.Body, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	got := req["messages"].([]any)
	if len(got) == 0 {
		t.Fatal("expected at least one message")
	}
	// First message must be the system message.
	first := got[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("first role = %v, want system", first["role"])
	}
	if first["content"] != "you are a helpful assistant" {
		t.Errorf("first content = %v, want system prompt", first["content"])
	}
}

func TestSanitizePreservesSystemMessage(t *testing.T) {
	msgs := []string{`{"role":"system","content":"you are a helpful assistant"}`}
	for i := 0; i < 120; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, fmt.Sprintf(`{"role":"%s","content":"msg %d"}`, role, i))
	}
	body := fmt.Sprintf(`{"model":"gpt-4","messages":[%s]}`, strings.Join(msgs, ","))

	res, err := sanitize.Sanitize(strings.NewReader(body), nil, &config.Config{MaxContextMessages: 100})
	if err != nil {
		t.Fatalf("sanitize error: %v", err)
	}
	var req map[string]any
	if err := json.Unmarshal(res.Body, &req); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	got := req["messages"].([]any)
	if len(got) != 100 {
		t.Fatalf("got %d messages, want 100", len(got))
	}

	// First message must be the system message.
	first := got[0].(map[string]any)
	if first["role"] != "system" {
		t.Errorf("first role = %v, want system", first["role"])
	}
	if first["content"] != "you are a helpful assistant" {
		t.Errorf("first content = %v, want system prompt", first["content"])
	}

	// Last message should be msg 119.
	last := got[len(got)-1].(map[string]any)
	if last["content"] != "msg 119" {
		t.Errorf("last message content = %v, want msg 119", last["content"])
	}
}
