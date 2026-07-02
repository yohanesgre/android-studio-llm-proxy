package forward

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yohanesgre/android-studio-llm-proxy/internal/cache"
)

// oneByteReader returns exactly 1 byte per Read call, simulating worst-case
// fragmented network delivery where each TCP segment carries a single byte.
type oneByteReader struct {
	data []byte
	off  int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	p[0] = r.data[r.off]
	r.off++
	return 1, nil
}

// recordingWriter captures each Write call separately so we can verify the
// proxy only writes complete SSE events (not partial chunks).
type recordingWriter struct {
	buf    bytes.Buffer
	writes [][]byte
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	cp := make([]byte, len(b))
	copy(cp, b)
	w.writes = append(w.writes, cp)
	return w.buf.Write(b)
}

func (w *recordingWriter) Flush() {}

func (w *recordingWriter) Header() http.Header { return http.Header{} }

func (w *recordingWriter) WriteHeader(int) {}

func TestStreamResponseBuffersUntilBlankLine(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	f := &Forwarder{cache: c}

	sseEvent := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"}}]}\n\n"

	reader := &oneByteReader{data: []byte(sseEvent)}
	w := &recordingWriter{}

	f.streamResponse(w, reader)

	body := w.buf.String()
	if !strings.Contains(body, "hello") {
		t.Fatal("expected content in output")
	}

	// With a 1-byte-at-a-time reader, the old code would have made ~90 writes
	// (one per byte). The new code should make exactly 1 write (the complete event).
	if len(w.writes) != 1 {
		t.Errorf("expected 1 write (complete event), got %d writes", len(w.writes))
		for i, wr := range w.writes {
			t.Logf("  write[%d] = %q", i, wr)
		}
	}

	// Every write must end with \n\n (complete SSE event).
	for i, wr := range w.writes {
		if !bytes.HasSuffix(wr, []byte("\n\n")) {
			t.Errorf("write %d does not end with \\n\\n: %q", i, wr)
		}
	}
}

func TestStreamResponseMultipleEventsOneByteReader(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	f := &Forwarder{cache: c}

	sseData := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n" +
		"data: [DONE]\n\n"

	reader := &oneByteReader{data: []byte(sseData)}
	w := &recordingWriter{}

	f.streamResponse(w, reader)

	body := w.buf.String()

	// reasoning_content events are NOT forwarded to the client.
	if strings.Contains(body, "think") {
		t.Error("reasoning_content should not be forwarded to client")
	}

	// [DONE] must still be forwarded.
	if !strings.Contains(body, "[DONE]") {
		t.Error("output missing [DONE]")
	}

	// Each write must end with \n\n.
	for i, wr := range w.writes {
		if !bytes.HasSuffix(wr, []byte("\n\n")) {
			t.Errorf("write %d does not end with \\n\\n: %q", i, wr)
		}
	}

	// Cache should still have the reasoning (accumulated from raw events).
	got, ok := c.Find("", nil)
	if !ok {
		t.Fatal("expected reasoning to be cached")
	}
	if got != "think" {
		t.Errorf("cached reasoning = %q, want %q", got, "think")
	}
}

func TestStreamResponsePartialEventAtEOF(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	f := &Forwarder{cache: c}

	// No trailing blank line — simulates upstream closing mid-stream.
	partial := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"orphan\"}}]}"

	reader := &oneByteReader{data: []byte(partial)}
	w := &recordingWriter{}

	f.streamResponse(w, reader)

	body := w.buf.String()
	if !strings.Contains(body, "orphan") {
		t.Fatal("expected partial event to be forwarded")
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("expected trailing \\n\\n, got body ending %q", body)
	}
}

func TestStreamCoalescesContentEvents(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	f := &Forwarder{cache: c}

	// 3 content-only events that should be coalesced into 1 synthetic event.
	sseData := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" from DeepSeek\"}}]}\n\n" +
		"data: [DONE]\n\n"

	reader := &oneByteReader{data: []byte(sseData)}
	w := &recordingWriter{}

	f.streamResponse(w, reader)

	body := w.buf.String()

	// Must contain the full concatenated content.
	if !strings.Contains(body, "Hello world from DeepSeek") {
		t.Errorf("expected coalesced content, got body: %s", body)
	}

	// Must NOT contain individual content events as separate data lines.
	// Count the number of data: lines with content (excluding [DONE]).
	dataLines := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.TrimPrefix(line, "data: ") != "[DONE]" {
			dataLines++
		}
	}
	if dataLines != 1 {
		t.Errorf("expected exactly 1 synthetic content event, got %d data lines", dataLines)
		for _, line := range strings.Split(body, "\n") {
			t.Logf("  %q", line)
		}
	}

	// [DONE] must be forwarded.
	if !strings.Contains(body, "[DONE]") {
		t.Error("expected [DONE] in output")
	}
}

func TestStreamDoesNotForwardReasoning(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	f := &Forwarder{cache: c}

	sseData := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"let me think\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\" about this\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"the answer\"}}]}\n\n" +
		"data: [DONE]\n\n"

	reader := &oneByteReader{data: []byte(sseData)}
	w := &recordingWriter{}

	f.streamResponse(w, reader)

	body := w.buf.String()

	// reasoning_content must NOT be in client output.
	if strings.Contains(body, "let me think") {
		t.Error("reasoning_content should not be forwarded to client")
	}
	if strings.Contains(body, "about this") {
		t.Error("reasoning_content should not be forwarded to client")
	}

	// Content must be in client output.
	if !strings.Contains(body, "the answer") {
		t.Error("expected content in output")
	}

	// Cache should have the reasoning_content.
	got, ok := c.Find("the answer", nil)
	if !ok {
		t.Fatal("expected reasoning to be cached")
	}
	if got != "let me think about this" {
		t.Errorf("cached reasoning = %q, want %q", got, "let me think about this")
	}
}

func TestStreamFlushesOnFinishReason(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	f := &Forwarder{cache: c}

	finishReason := "stop"
	sseData := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"" + finishReason + "\"}]}\n\n" +
		"data: [DONE]\n\n"

	reader := &oneByteReader{data: []byte(sseData)}
	w := &recordingWriter{}

	f.streamResponse(w, reader)

	body := w.buf.String()

	// Content must be flushed as synthetic event before the finish_reason event.
	if !strings.Contains(body, "Hello world") {
		t.Errorf("expected coalesced content, got body: %s", body)
	}

	// finish_reason event must be present.
	if !strings.Contains(body, "stop") {
		t.Error("expected finish_reason in output")
	}

	// Verify ordering: content event comes before finish_reason event.
	contentIdx := strings.Index(body, "Hello world")
	finishIdx := strings.Index(body, "stop")
	if contentIdx == -1 || finishIdx == -1 {
		t.Fatal("missing content or finish_reason in output")
	}
	if contentIdx >= finishIdx {
		t.Error("expected content to be flushed before finish_reason event")
	}
}

func TestStreamFlushesOnToolCalls(t *testing.T) {
	c := cache.NewMemoryCache(time.Hour, 100)
	f := &Forwarder{cache: c}

	sseData := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Let me\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" check\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"tc1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: [DONE]\n\n"

	reader := &oneByteReader{data: []byte(sseData)}
	w := &recordingWriter{}

	f.streamResponse(w, reader)

	body := w.buf.String()

	// Content must be flushed as synthetic event before tool_calls.
	if !strings.Contains(body, "Let me check") {
		t.Errorf("expected coalesced content, got body: %s", body)
	}

	// tool_calls event must be present.
	if !strings.Contains(body, "get_weather") {
		t.Error("expected tool_calls in output")
	}

	// Verify ordering: content event comes before tool_calls event.
	contentIdx := strings.Index(body, "Let me check")
	toolIdx := strings.Index(body, "get_weather")
	if contentIdx == -1 || toolIdx == -1 {
		t.Fatal("missing content or tool_calls in output")
	}
	if contentIdx >= toolIdx {
		t.Error("expected content to be flushed before tool_calls event")
	}
}
