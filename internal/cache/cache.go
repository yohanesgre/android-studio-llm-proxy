// Package cache provides caching for reasoning_content from assistant messages.
//
// The cache stores reasoning_content from model responses and re-injects it
// into subsequent requests when Android Studio drops it from conversation history.
// This is necessary because some models (DeepSeek V4, Kimi K2.7) require
// reasoning_content to be passed back in multi-turn conversations.
package cache

// ToolCall represents a tool call in a message.
type ToolCall struct {
	ID       string
	Type     string
	Function struct {
		Name      string
		Arguments string // raw JSON string
	}
}

// Message represents a cached message with reasoning content.
type Message struct {
	Role             string
	Content          string
	ToolCalls        []ToolCall
	ReasoningContent string
}

// ReasoningCache caches reasoning_content from assistant messages.
type ReasoningCache interface {
	// Store caches a message that has reasoning_content.
	Store(msg Message)
	// Find looks up a cached reasoning_content by message fingerprint.
	// Returns the reasoning_content and true if found.
	Find(content string, toolCalls []ToolCall) (string, bool)
}
