package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type entry struct {
	reasoning  string
	lastAccess time.Time
	content    string
	toolCalls  []ToolCall
}

// MemoryCache is an in-memory ReasoningCache with TTL and LRU eviction.
type MemoryCache struct {
	mu            sync.RWMutex
	items         map[string]*entry
	byToolCallIDs map[string]*entry // key: sorted tool call IDs joined by "|"
	byContent     map[string]*entry // key: normalized content
	order         []string          // keys in insertion order for eviction
	ttl           time.Duration
	maxEntries    int
}

// NewMemoryCache creates a MemoryCache with the given TTL and max entries.
func NewMemoryCache(ttl time.Duration, maxEntries int) *MemoryCache {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	return &MemoryCache{
		items:         make(map[string]*entry),
		byToolCallIDs: make(map[string]*entry),
		byContent:     make(map[string]*entry),
		order:         make([]string, 0, maxEntries),
		ttl:           ttl,
		maxEntries:    maxEntries,
	}
}

func (c *MemoryCache) key(content string, toolCalls []ToolCall) string {
	serialized, _ := json.Marshal(toolCalls)
	h := sha256.New()
	h.Write([]byte("assistant\n"))
	h.Write([]byte(content))
	h.Write([]byte("\n"))
	h.Write(serialized)
	return hex.EncodeToString(h.Sum(nil))
}

func (c *MemoryCache) toolCallIDsKey(toolCalls []ToolCall) string {
	if len(toolCalls) == 0 {
		return ""
	}
	ids := make([]string, len(toolCalls))
	for i, tc := range toolCalls {
		ids[i] = tc.ID
	}
	sort.Strings(ids)
	return strings.Join(ids, "|")
}

var whitespaceRegex = regexp.MustCompile(`\s+`)

func normalizeContent(content string) string {
	return strings.TrimSpace(whitespaceRegex.ReplaceAllString(content, " "))
}

// Store caches a message that has reasoning_content.
func (c *MemoryCache) Store(msg Message) {
	if msg.ReasoningContent == "" {
		return
	}

	k := c.key(msg.Content, msg.ToolCalls)
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	slog.Debug("reasoning cache store",
		"key", k[:16],
		"content_len", len(msg.Content),
		"reasoning_len", len(msg.ReasoningContent),
		"tool_calls", len(msg.ToolCalls),
	)

	if e, ok := c.items[k]; ok {
		e.lastAccess = now
		c.touchOrder(k)
		return
	}

	// Evict oldest if at capacity.
	for len(c.items) >= c.maxEntries && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.evict(oldest)
	}

	e := &entry{
		reasoning:  msg.ReasoningContent,
		lastAccess: now,
		content:    msg.Content,
		toolCalls:  msg.ToolCalls,
	}
	c.items[k] = e
	c.order = append(c.order, k)

	// Add to secondary indexes.
	if tcKey := c.toolCallIDsKey(msg.ToolCalls); tcKey != "" {
		c.byToolCallIDs[tcKey] = e
	}
	if normContent := normalizeContent(msg.Content); normContent != "" {
		c.byContent[normContent] = e
	}
}

// Find looks up a cached reasoning_content by message fingerprint.
// Tries multiple strategies in order: exact match, tool-call-ID match,
// content match, fuzzy content match.
func (c *MemoryCache) Find(content string, toolCalls []ToolCall) (string, bool) {
	k := c.key(content, toolCalls)

	c.mu.Lock()
	defer c.mu.Unlock()

	slog.Debug("reasoning cache find attempt",
		"key", k[:16],
		"content_len", len(content),
		"tool_calls", len(toolCalls),
	)

	// Strategy 1: Exact match.
	if e, ok := c.items[k]; ok {
		if time.Since(e.lastAccess) > c.ttl {
			c.evict(k)
		} else {
			e.lastAccess = time.Now()
			c.touchOrder(k)
			slog.Debug("reasoning cache hit (exact)", "key", k[:16], "reasoning_len", len(e.reasoning))
			return e.reasoning, true
		}
	}

	// Strategy 2: Tool-call-ID match.
	if len(toolCalls) > 0 {
		tcKey := c.toolCallIDsKey(toolCalls)
		if e, ok := c.byToolCallIDs[tcKey]; ok {
			if time.Since(e.lastAccess) > c.ttl {
				ek := c.key(e.content, e.toolCalls)
				c.evict(ek)
			} else {
				e.lastAccess = time.Now()
				ek := c.key(e.content, e.toolCalls)
				c.touchOrder(ek)
				slog.Debug("reasoning cache hit (tool-call-id)", "key", ek[:16], "reasoning_len", len(e.reasoning))
				return e.reasoning, true
			}
		}
	}

	// Strategy 3: Content match.
	if content != "" {
		normContent := normalizeContent(content)
		if e, ok := c.byContent[normContent]; ok {
			if time.Since(e.lastAccess) > c.ttl {
				ek := c.key(e.content, e.toolCalls)
				c.evict(ek)
			} else {
				e.lastAccess = time.Now()
				ek := c.key(e.content, e.toolCalls)
				c.touchOrder(ek)
				slog.Debug("reasoning cache hit (content)", "key", ek[:16], "reasoning_len", len(e.reasoning))
				return e.reasoning, true
			}
		}
	}

	slog.Debug("reasoning cache miss", "content_len", len(content), "tool_calls", len(toolCalls))
	return "", false
}

// evict removes an entry from all indexes. Must be called with lock held.
func (c *MemoryCache) evict(key string) {
	if e, ok := c.items[key]; ok {
		// Remove from secondary indexes.
		if tcKey := c.toolCallIDsKey(e.toolCalls); tcKey != "" {
			delete(c.byToolCallIDs, tcKey)
		}
		if normContent := normalizeContent(e.content); normContent != "" {
			// Only delete if this entry is still the one in the index.
			if c.byContent[normContent] == e {
				delete(c.byContent, normContent)
			}
		}
	}
	delete(c.items, key)
}

func (c *MemoryCache) removeFromOrder(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// touchOrder moves key to the end of the order slice (most recently used).
func (c *MemoryCache) touchOrder(key string) {
	c.removeFromOrder(key)
	c.order = append(c.order, key)
}
