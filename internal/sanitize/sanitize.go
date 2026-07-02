// Package sanitize provides request body sanitization for different model families.
//
// Android Studio's AI Agent sends requests with non-standard fields and formats
// that some upstream models reject. This package cleans up those requests before
// forwarding them to the upstream LLM API.
//
// Sanitization includes:
//   - Stripping OpenAIAPI/models/ prefix from model field
//   - Mapping role "developer" to "system"
//   - Removing unsupported sampling parameters per model family
//   - Normalizing tool_choice values
//   - Parsing tool arguments from string to JSON object
//   - Injecting reasoning_content from cache for multi-turn conversations
package sanitize

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/yohanesgre/android-studio-llm-proxy/internal/cache"
	"github.com/yohanesgre/android-studio-llm-proxy/internal/config"
)

// Result holds the sanitized body and whether the request is streaming.
type Result struct {
	Body     []byte
	IsStream bool
}

// estimateTokens returns a rough token count for a string using 4 chars/token heuristic.
// This is deliberately imprecise — good enough for context-budget enforcement.
func estimateTokens(s string) int {
	return len([]rune(s)) / 4
}

// estimateMessageTokens returns an estimated token count for a message map.
// Includes ~4 tokens overhead for message metadata (role, etc.).
func estimateMessageTokens(m any) int {
	msg, ok := m.(map[string]any)
	if !ok {
		return 4
	}
	tokens := 4 // message overhead
	if content, ok := msg["content"].(string); ok {
		tokens += estimateTokens(content)
	}
	if rc, ok := msg["reasoning_content"].(string); ok {
		tokens += estimateTokens(rc)
	}
	// Tool calls add nominal overhead.
	if tcs, ok := msg["tool_calls"].([]any); ok {
		tokens += len(tcs) * 20
	}
	return tokens
}

// Sanitize reads a chat-completion request body, applies model-specific
// sanitization rules, and returns the cleaned body.
func Sanitize(r io.Reader, c cache.ReasoningCache, cfg *config.Config) (*Result, error) {
	start := time.Now()
	var req map[string]any
	if err := json.NewDecoder(r).Decode(&req); err != nil {
		return nil, fmt.Errorf("sanitize: invalid json: %w", err)
	}

	// Strip OpenAIAPI/models/ prefix from model field.
	if model, ok := req["model"].(string); ok {
		req["model"] = strings.TrimPrefix(model, "OpenAIAPI/models/")
	}

	// Map role "developer" → "system" in all messages.
	if msgs, ok := req["messages"].([]any); ok {
		for _, m := range msgs {
			if msg, ok := m.(map[string]any); ok {
				if role, ok := msg["role"].(string); ok && role == "developer" {
					msg["role"] = "system"
				}
			}
		}
	}

	// Detect model family and apply rules.
	model, _ := req["model"].(string)

	// Apply context budget: token-based (preferred) or message-count fallback.
	// Per-model overrides take precedence over global config.
	if cfg != nil {
		if maxTokens := resolveMaxContextTokens(cfg, model); maxTokens > 0 {
			trimByTokens(req, maxTokens)
		} else if cfg.MaxContextMessages > 0 {
			trimMessages(req, cfg.MaxContextMessages)
		}
	}

	// Inject max_completion_tokens if not set by the client.
	injectMaxCompletionTokens(req, cfg, model)
	family := detectFamily(model)

	// Apply per-model overrides BEFORE family rules.
	if cfg != nil && cfg.Models != nil {
		if modelOverrides, ok := cfg.Models[model]; ok {
			applyOverrides(req, modelOverrides)
		}
	}

	// Normalize consistent `thinking: true/false` to provider-specific format.
	normalizeThinking(req, family)

	// Strip image URLs for models that don't support vision.
	stripImageURLs(req, family)

	applyRules(req, family)

	// Inject reasoning_content from cache for assistant messages.
	// This must happen AFTER applyRules so that DeepSeek Reasoner's
	// stripReasoningContent runs first, then we inject the placeholder.
	if c != nil {
		injectReasoning(req, c, model)
	}

	isStream, _ := req["stream"].(bool)

	out, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("sanitize: marshal: %w", err)
	}

	// Log sanitization duration.
	duration := time.Since(start)
	slog.Info("sanitize",
		"duration_ms", float64(duration.Microseconds())/1000.0,
		"model", model,
	)

	return &Result{Body: out, IsStream: isStream}, nil
}

type family int

// trimMessages trims the oldest non-system messages when the total count exceeds max.
// If the first message has role "system", it is preserved and the most recent max-1 messages are kept.
// Otherwise, the most recent max messages are kept.
func trimMessages(req map[string]any, max int) {
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) <= max {
		return
	}

	hasSystem := false
	if first, ok := msgs[0].(map[string]any); ok {
		if role, ok := first["role"].(string); ok && role == "system" {
			hasSystem = true
		}
	}

	if hasSystem {
		system := msgs[0]
		keep := max - 1
		if keep < 0 {
			keep = 0
		}
		trimmed := make([]any, 0, 1+keep)
		trimmed = append(trimmed, system)
		if keep > 0 {
			trimmed = append(trimmed, msgs[len(msgs)-keep:]...)
		}
		req["messages"] = trimmed
	} else {
		req["messages"] = msgs[len(msgs)-max:]
	}
}

// trimByTokens trims oldest non-system messages until the total estimated tokens
// is within maxTokens. The system message at position 0 is always preserved.
func trimByTokens(req map[string]any, maxTokens int) {
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return
	}

	// Check if first message is system.
	preserveSystem := false
	if first, ok := msgs[0].(map[string]any); ok {
		if role, _ := first["role"].(string); role == "system" {
			preserveSystem = true
		}
	}

	// Walk from newest to oldest, collecting messages that fit.
	remaining := maxTokens

	if preserveSystem {
		sysTokens := estimateMessageTokens(msgs[0])
		if sysTokens >= remaining {
			// System message alone exceeds budget — keep it anyway as minimum.
			req["messages"] = []any{msgs[0]}
			return
		}
		remaining -= sysTokens
	}

	start := 0
	if preserveSystem {
		start = 1
	}

	var kept []any
	for i := len(msgs) - 1; i >= start; i-- {
		t := estimateMessageTokens(msgs[i])
		if t <= remaining {
			kept = append(kept, msgs[i])
			remaining -= t
		} else {
			// If we can't fit even the most recent, keep it anyway.
			if len(kept) == 0 {
				kept = append(kept, msgs[i])
			}
			break
		}
	}

	// Reverse to maintain chronological order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	if preserveSystem {
		result := make([]any, 0, 1+len(kept))
		result = append(result, msgs[0])
		result = append(result, kept...)
		req["messages"] = result
	} else {
		req["messages"] = kept
	}
}

// resolveMaxContextTokens returns the effective max_context_tokens budget for a model.
// Per-model overrides take precedence over global config, then 0 (disabled).
func resolveMaxContextTokens(cfg *config.Config, model string) int {
	if cfg.Models != nil {
		if overrides, ok := cfg.Models[model]; ok {
			if v, ok := overrides["max_context_tokens"]; ok {
				if n, ok := v.(float64); ok && n > 0 {
					return int(n)
				}
			}
		}
	}
	return cfg.MaxContextTokens
}

// injectMaxCompletionTokens sets max_completion_tokens in the request if absent
// AND the user has explicitly configured a limit. Only injects for DeepSeek models.
// No default is applied — unbounded generation is the model's responsibility
// unless the user opts in via config.
func injectMaxCompletionTokens(req map[string]any, cfg *config.Config, model string) {
	// Already set (by client or per-model override from applyOverrides) — respect it.
	if _, ok := req["max_completion_tokens"]; ok {
		return
	}

	family := detectFamily(model)
	if family != familyDeepSeekV4 && family != familyDeepSeekReasoner {
		return
	}

	// Only inject when user explicitly configured a limit.
	if cfg == nil || cfg.MaxCompletionTokens <= 0 {
		return
	}
	req["max_completion_tokens"] = cfg.MaxCompletionTokens
}

const (
	familyUnknown family = iota
	familyKimiK27
	familyKimiK26
	familyDeepSeekV4
	familyDeepSeekReasoner
	familyQwen37
)

func detectFamily(model string) family {
	switch model {
	case "kimi-k2.7-code", "kimi-k2.7-code-highspeed", "kimi-k2.7":
		return familyKimiK27
	case "kimi-k2.6":
		return familyKimiK26
	case "deepseek-v4-pro", "deepseek-v4-flash":
		return familyDeepSeekV4
	case "deepseek-reasoner":
		return familyDeepSeekReasoner
	case "qwen3.7-plus", "qwen3.7-max":
		return familyQwen37
	default:
		return familyUnknown
	}
}

func applyRules(req map[string]any, f family) {
	thinkingDisabled := isThinkingDisabled(req)

	switch f {
	case familyKimiK27:
		// K2.7 cannot disable thinking, always strip sampling params.
		delete(req, "temperature")
		delete(req, "top_p")
		delete(req, "presence_penalty")
		delete(req, "frequency_penalty")
		delete(req, "n")
		normalizeToolChoice(req)

	case familyKimiK26:
		// K2.6 can disable thinking; if so, allow sampling params.
		if !thinkingDisabled {
			delete(req, "temperature")
			delete(req, "top_p")
			delete(req, "presence_penalty")
			delete(req, "frequency_penalty")
			delete(req, "n")
		}
		normalizeToolChoice(req)

	case familyDeepSeekV4:
		if !thinkingDisabled {
			normalizeToolChoice(req)
		}

	case familyDeepSeekReasoner:
		normalizeToolChoice(req)
		stripReasoningContent(req)

	case familyQwen37:
		if !thinkingDisabled {
			normalizeToolChoice(req)
		}
		parseToolArguments(req)
	}
}

// normalizeToolChoice ensures tool_choice is "auto" or "none".
// If it's anything else (including an object or "required"), set to "auto".
func normalizeToolChoice(req map[string]any) {
	tc, ok := req["tool_choice"]
	if !ok {
		return
	}
	s, ok := tc.(string)
	if ok && (s == "auto" || s == "none") {
		return
	}
	req["tool_choice"] = "auto"
}

// stripReasoningContent removes reasoning_content from all input messages.
func stripReasoningContent(req map[string]any) {
	msgs, ok := req["messages"].([]any)
	if !ok {
		return
	}
	for _, m := range msgs {
		if msg, ok := m.(map[string]any); ok {
			delete(msg, "reasoning_content")
		}
	}
}

// parseToolArguments parses tools[].function.arguments from string to JSON object.
func parseToolArguments(req map[string]any) {
	tools, ok := req["tools"].([]any)
	if !ok {
		return
	}
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		args, ok := fn["arguments"]
		if !ok {
			continue
		}
		s, ok := args.(string)
		if !ok {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			continue // leave as-is if not valid JSON
		}
		fn["arguments"] = parsed
	}
}

// injectReasoning looks up cached reasoning_content for assistant messages
// that are missing it and injects it back.
func injectReasoning(req map[string]any, c cache.ReasoningCache, model string) {
	msgs, ok := req["messages"].([]any)
	if !ok {
		return
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}
		// Skip if already has reasoning_content.
		if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
			continue
		}

		content, _ := msg["content"].(string)
		toolCalls := extractToolCalls(msg)

		reasoning, found := c.Find(content, toolCalls)
		if !found {
			slog.Debug("reasoning_content not in cache",
				"model", model,
				"content_len", len(content),
				"has_tool_calls", len(toolCalls) > 0,
			)
			// DeepSeek requires reasoning_content on assistant messages for multi-turn.
			// Use a minimal bracket-framed placeholder to satisfy API format validation.
			if isDeepSeekModel(model) {
				msg["reasoning_content"] = "[...]"
			}
			continue
		}
		msg["reasoning_content"] = reasoning
	}
}

// extractToolCalls extracts tool calls from a message map into cache.ToolCall slice.
func extractToolCalls(msg map[string]any) []cache.ToolCall {
	raw, ok := msg["tool_calls"].([]any)
	if !ok {
		return nil
	}
	var calls []cache.ToolCall
	for _, item := range raw {
		tc, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var call cache.ToolCall
		call.ID, _ = tc["id"].(string)
		call.Type, _ = tc["type"].(string)
		if fn, ok := tc["function"].(map[string]any); ok {
			call.Function.Name, _ = fn["name"].(string)
			switch v := fn["arguments"].(type) {
			case string:
				call.Function.Arguments = v
			default:
				// If it's an object or other type, marshal to JSON string.
				if b, err := json.Marshal(v); err == nil {
					call.Function.Arguments = string(b)
				}
			}
		}
		calls = append(calls, call)
	}
	return calls
}

// applyOverrides merges model-specific overrides into the request body.
func applyOverrides(req map[string]any, overrides map[string]any) {
	for k, v := range overrides {
		req[k] = v
	}
}

// normalizeThinking converts a consistent `thinking: true/false` override into
// the provider-specific format expected by each model family.
//
// Supported families:
//   - DeepSeek V4 / Reasoner: thinking: true  -> thinking: {"type": "enabled"}
//   - DeepSeek V4 / Reasoner: thinking: false -> thinking: {"type": "disabled"}
//   - Kimi K2.6:               thinking: true  -> thinking: {"type": "enabled"}
//   - Kimi K2.6:               thinking: false -> thinking: {"type": "disabled"}
//   - Qwen 3.7:                thinking: true  -> enable_thinking: true
//   - Qwen 3.7:                thinking: false -> enable_thinking: false
//
// Provider-specific objects (e.g. thinking: {"type": "enabled"}) are passed
// through unchanged, so power users can still set the raw upstream format.
func normalizeThinking(req map[string]any, family family) {
	thinking, ok := req["thinking"].(bool)
	if !ok {
		return
	}

	switch family {
	case familyDeepSeekV4, familyDeepSeekReasoner, familyKimiK26, familyKimiK27:
		if thinking {
			req["thinking"] = map[string]any{"type": "enabled"}
		} else {
			req["thinking"] = map[string]any{"type": "disabled"}
		}
	case familyQwen37:
		req["enable_thinking"] = thinking
		delete(req, "thinking")
	default:
		// Unknown family: don't send a bare boolean that upstream may reject.
		delete(req, "thinking")
	}
}

// stripImageURLs removes image_url content items from messages when the target
// model does not support vision. Text content is preserved.
func stripImageURLs(req map[string]any, family family) {
	if supportsVision(family) {
		return
	}

	msgs, ok := req["messages"].([]any)
	if !ok {
		return
	}

	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}

		content, ok := msg["content"]
		if !ok {
			continue
		}

		switch v := content.(type) {
		case []any:
			filtered := make([]any, 0, len(v))
			for _, item := range v {
				part, ok := item.(map[string]any)
				if !ok {
					filtered = append(filtered, item)
					continue
				}
				if t, _ := part["type"].(string); t == "image_url" {
					continue
				}
				filtered = append(filtered, item)
			}
			if len(filtered) == 0 {
				msg["content"] = ""
			} else {
				msg["content"] = filtered
			}
		}
	}
}

// supportsVision returns true for model families that accept image_url content.
func supportsVision(f family) bool {
	switch f {
	case familyKimiK26, familyKimiK27, familyQwen37:
		return true
	default:
		return false
	}
}

// isThinkingDisabled checks if thinking is disabled via overrides.
// Returns true if:
// - thinking: false at top level, OR
// - thinking.type == "disabled" at top level, OR
// - enable_thinking == false at top level (for Qwen)
func isThinkingDisabled(req map[string]any) bool {
	// Check thinking: false
	if thinking, ok := req["thinking"].(bool); ok {
		return !thinking
	}

	// Check thinking.type == "disabled"
	if thinking, ok := req["thinking"].(map[string]any); ok {
		if t, ok := thinking["type"].(string); ok && t == "disabled" {
			return true
		}
	}

	// Check enable_thinking == false (Qwen style)
	if et, ok := req["enable_thinking"].(bool); ok && !et {
		return true
	}

	return false
}

// isDeepSeekModel returns true if the model is a DeepSeek model that requires reasoning_content.
func isDeepSeekModel(model string) bool {
	family := detectFamily(model)
	return family == familyDeepSeekV4 || family == familyDeepSeekReasoner
}
