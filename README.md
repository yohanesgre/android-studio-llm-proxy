# Android Studio LLM Proxy

![Go Version](https://img.shields.io/badge/go-1.24+-blue)
![Tests](https://img.shields.io/badge/tests-passing-green)
![License](https://img.shields.io/badge/license-MIT-blue)

A tiny local proxy that lets **Android Studio** use **[OpenCode Go / Zen](https://opencode.ai/zen/go/v1)** models.

Android Studio speaks OpenAI-compatible API, but its requests contain extra fields and prefixes that OpenCode Go models reject. This proxy sits between them, cleans up the requests, and forwards them upstream.

## Table of Contents

- [Features](#features)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Supported Models](#supported-models)
- [Android Studio Setup](#android-studio-setup)
- [Architecture / Request Flow](#architecture--request-flow)
- [Sanitization Rules](#sanitization-rules)
- [Performance Logging](#performance-logging)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Features

- **Request sanitization** — Strips non-standard fields and normalizes request format
- **Multi-model support** — DeepSeek V4, Kimi K2.7/K2.6, Qwen 3.7, and more
- **Per-model configuration** — Override settings like `thinking` and `reasoning_effort` via config file
- **Reasoning content caching** — Automatically caches and re-injects `reasoning_content` for multi-turn conversations
- **Image URL stripping** — Removes `image_url` content for non-vision models automatically
- **Streaming support** — Full SSE streaming with real-time caching
- **Performance logging** — Track request duration, upstream latency, and sanitization overhead
- **Zero dependencies** — Built with Go standard library only

## Installation

### Option 1: Download Binary (Recommended)

```bash
# macOS (Apple Silicon)
curl -L https://github.com/yohanesgre/android-studio-llm-proxy/releases/latest/download/android-studio-llm-proxy-darwin-arm64 -o android-studio-llm-proxy
chmod +x android-studio-llm-proxy
sudo mv android-studio-llm-proxy /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/yohanesgre/android-studio-llm-proxy/releases/latest/download/android-studio-llm-proxy-darwin-amd64 -o android-studio-llm-proxy
chmod +x android-studio-llm-proxy
sudo mv android-studio-llm-proxy /usr/local/bin/

# Linux (x86_64)
curl -L https://github.com/yohanesgre/android-studio-llm-proxy/releases/latest/download/android-studio-llm-proxy-linux-amd64 -o android-studio-llm-proxy
chmod +x android-studio-llm-proxy
sudo mv android-studio-llm-proxy /usr/local/bin/
```

### Option 2: Install with Go

```bash
go install github.com/yohanesgre/android-studio-llm-proxy/cmd/proxy@latest
```

### Option 3: Build from Source

```bash
git clone https://github.com/yohanesgre/android-studio-llm-proxy.git
cd android-studio-llm-proxy
just build
sudo cp bin/android-studio-llm-proxy /usr/local/bin/
```

### Option 4: Docker / Docker Compose

```bash
# Using docker-compose (recommended)
docker compose up --build -d

# Or build and run manually
docker build -t android-studio-llm-proxy:latest .
docker run --rm -p 9999:9999 \
  -v ${HOME}/.config/android-studio-llm-proxy:/home/proxy/.config/android-studio-llm-proxy \
  android-studio-llm-proxy:latest
```

The proxy will create a default `config.json` on first run if one does not exist.

**Note for Linux users:** the container runs as UID `1000`. If the host config directory already exists, ensure it is writable by that UID, or let Docker create it for you.

### Option 5: Apple Container

For [apple/container](https://github.com/apple/container) on macOS (uses the `container` CLI, not Docker):

```bash
just apple-container-build
just apple-container-run
```

Or run the `container` commands directly:

```bash
container build -f Containerfile -t android-studio-llm-proxy:0.1.0 .
container delete -f android-studio-llm-proxy || true
container run --rm -p 9999:9999 --name android-studio-llm-proxy \
  -v ${HOME}/.config/android-studio-llm-proxy:/home/proxy/.config/android-studio-llm-proxy \
  android-studio-llm-proxy:0.1.0
```

If you see `Address already in use`, a stale container is likely still holding the vsock. Run `container delete -f android-studio-llm-proxy` or `container prune`, then try again.

## Quick Start

1. **Start the proxy:**
   ```bash
   # If installed to PATH
   android-studio-llm-proxy

   # Or run from build directory
   ./bin/android-studio-llm-proxy
   ```
   The proxy listens on `http://localhost:9999` by default.

   On first run, it creates a default config file at `~/.config/android-studio-llm-proxy/config.json` from the bundled `config.example.json`. You can edit this file to customize model parameters.

2. **Configure Android Studio** (see [Android Studio Setup](#android-studio-setup) below)

3. **Start chatting** with your favorite OpenCode Go models!

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `9999` | Port to listen on |
| `UPSTREAM_URL` | `https://opencode.ai/zen/go/v1` | Upstream LLM API base URL |
| `CONFIG_PATH` | `$HOME/.config/android-studio-llm-proxy/config.json` | Path to config file |
| `CACHE_TTL` | `1h` | TTL for reasoning_content cache |
| `CACHE_MAX_ENTRIES` | `1000` | Max entries in reasoning_content cache |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |

**Note on API keys:** The proxy does **not** read an `API_KEY` environment variable. It passes through the `Authorization` header sent by Android Studio, so you configure your API key in Android Studio's provider settings.

### Per-Model Configuration

The proxy loads model-specific overrides from `~/.config/android-studio-llm-proxy/config.json`. On first run, this file is created automatically from the bundled defaults. You can edit it to customize model parameters:

```json
{
  "models": {
    "deepseek-v4-flash": {
      "thinking": true,
      "reasoning_effort": "max"
    },
    "deepseek-v4-pro": {
      "thinking": true,
      "reasoning_effort": "max"
    },
    "kimi-k2.6": {
      "thinking": false
    },
    "qwen3.7-plus": {
      "thinking": true
    }
  }
}
```

**How it works:**
- Top-level fields under each model ID are merged into the chat-completion request body
- The model ID key must match the model after stripping the `OpenAIAPI/models/` prefix
- Overrides are applied BEFORE family-specific sanitization rules
- Use a consistent `thinking: true/false` boolean for all models; the proxy translates it to the provider-specific format automatically:
  - DeepSeek/Kimi: `thinking: {"type": "enabled"}` or `{"type": "disabled"}`
  - Qwen: `enable_thinking: true` or `enable_thinking: false`
- When `thinking: false` (or provider-specific disabled format):
  - `tool_choice` normalization is skipped (allows `"required"` or other values)
  - For Kimi K2.6: sampling params (`temperature`, `top_p`, etc.) are preserved
  - For DeepSeek V4: `tool_choice` is not normalized to `"auto"`

See `config.example.json` for a complete example.

## Supported Models

These are the models that have been tested with this proxy. Any OpenAI-compatible model served through OpenCode Go / Zen will likely work — sanitization rules are applied based on the detected model family.

| Model | Thinking | Reasoning | Tool Calls | Notes |
|-------|----------|-----------|------------|-------|
| `deepseek-v4-flash` | ✅ | ✅ | ✅ | Fast, cost-effective |
| `deepseek-v4-pro` | ✅ | ✅ | ✅ | High quality, slower |
| `kimi-k2.6` | ⚙️ | ⚙️ | ✅ | Configurable thinking |
| `qwen3.7-plus` | ✅ | ✅ | ✅ | Balanced performance |

**Legend:**
- ✅ = Tested and supported
- ⚙️ = Configurable via config file

### Known Issues

- **DeepSeek V4**: If upstream doesn't return `reasoning_content`, the proxy injects a placeholder to prevent 400 errors in multi-turn conversations
- **Kimi K2.7**: Cannot disable thinking (always enabled by model)
- **Vision models**: `image_url` fields are stripped automatically for non-vision models (DeepSeek V4, DeepSeek Reasoner)

## Android Studio Setup

Tested on **Android Studio Quail 1 | 2026.1.1 Patch 2** (Build #AI-261.23567.138.2611.15646644). Older or newer versions may work but have not been verified.

1. **Start the proxy:**
   ```bash
   android-studio-llm-proxy
   # or
   ./bin/android-studio-llm-proxy
   ```

2. **Add the proxy as a model provider:**
   - Open **Settings → Tools → AI → Model Providers**
   - Click the **+** button to add a new provider
   - Fill in the provider details:
     - **Description:** `OpenCode Go Proxy` (or any name)
     - **URL:** `http://localhost:9999/v1`
     - **URL Schema:** `OpenAI-compatible`
     - **API key:** your OpenCode Go / Zen API key (sent to upstream via the proxy)
   - Click **Refresh** to fetch the available models
   - Enable the models you want to use (e.g., `deepseek-v4-pro`, `deepseek-v4-flash`, `kimi-k2.7-code`, `qwen3.7-plus`)
   - Click **OK**

3. **Start chatting:**
   - Open the AI chat panel in Android Studio
   - Select one of the enabled models
   - Send a test message and check the proxy logs for request/response details

## Architecture / Request Flow

```
Android Studio
     │
     ▼
POST http://localhost:9999/v1/chat/completions
     │
     ▼
┌─────────────────────────────────────┐
│  HTTP server (cmd/proxy/main.go)    │
│  - load config                      │
│  - init reasoning cache             │
│  - route to /v1/chat/completions    │
└─────────────┬───────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│  Sanitize (internal/sanitize)       │
│  - strip OpenAIAPI/models/ prefix   │
│  - map developer → system           │
│  - apply per-model overrides        │
│  - normalize thinking               │
│  - strip image_url for non-vision   │
│  - apply family-specific rules      │
│  - inject cached reasoning_content  │
└─────────────┬───────────────────────┘
              │
              ▼
┌─────────────────────────────────────┐
│  Forward (internal/forward)         │
│  - send to OpenCode Go / Zen        │
│  - handle streaming (SSE) or        │
│    non-streaming response           │
│  - cache reasoning_content          │
└─────────────┬───────────────────────┘
              │
              ▼
   OpenCode Go / Zen
   (https://opencode.ai/zen/go/v1)
```

### Startup flow

1. Parse CLI flags (`-version`, `-v`)
2. Load config from `CONFIG_PATH` or `~/.config/android-studio-llm-proxy/config.json`; create default if missing
3. Override config values with environment variables: `PORT`, `UPSTREAM_URL`, `CACHE_TTL`, `CACHE_MAX_ENTRIES`, `LOG_LEVEL`
4. Initialize the in-memory reasoning cache
5. Start HTTP server on the configured port

### Request handling flow

1. Android Studio sends an OpenAI-compatible chat-completion request
2. The proxy receives it at `POST /v1/chat/completions`
3. The request body is decoded and sanitized for the target model family
4. The sanitized request is forwarded to the upstream OpenCode Go / Zen API
5. The upstream response is streamed or returned as-is
6. Any `reasoning_content` in the response is cached for the next turn
7. The response is returned to Android Studio in OpenAI-compatible format

### Reasoning content cache

Some models (notably DeepSeek) require `reasoning_content` from previous assistant messages to be present on follow-up turns. The proxy caches this content keyed by message content + tool calls, and re-injects it on the next request. If the upstream omits `reasoning_content`, a placeholder is injected to keep multi-turn conversations stable.

## Sanitization Rules

### All Requests
- Strip `OpenAIAPI/models/` prefix from `model` field
- Map `role: "developer"` → `role: "system"`
- Strip `image_url` content items for non-vision models (DeepSeek V4 / Reasoner); vision models (Kimi K2.7/K2.6, Qwen 3.7) keep them

### Kimi K2.7 / K2.6
- Remove `temperature`, `top_p`, `presence_penalty`, `frequency_penalty`, `n` (unless thinking is disabled for K2.6)
- Normalize `tool_choice` to `"auto"` if not `"auto"` or `"none"` (unless thinking is disabled)

### DeepSeek V4
- Normalize `tool_choice` to `"auto"` if not `"auto"` or `"none"` (unless thinking is disabled)
- Inject placeholder `reasoning_content` if upstream doesn't provide it

### DeepSeek Reasoner
- Same `tool_choice` rule as DeepSeek V4
- Strip `reasoning_content` from input messages
- Same placeholder injection as DeepSeek V4

### Qwen 3.7
- Same `tool_choice` rule (unless thinking is disabled)
- Parse `tools[].function.arguments` from string to JSON object

## Performance Logging

The proxy logs performance metrics at three levels:

### Request Log (INFO)
```json
{"level":"INFO","msg":"request","method":"POST","path":"/v1/chat/completions","model":"deepseek-v4-flash","status":200,"duration_ms":2450}
```

### Sanitize Log (INFO)
```json
{"level":"INFO","msg":"sanitize","duration_ms":0.15,"model":"deepseek-v4-flash"}
```

### Upstream Log (INFO)
```json
{"level":"INFO","msg":"upstream","url":"https://opencode.ai/zen/go/v1/chat/completions","status":200,"first_byte_ms":1200,"total_ms":2450}
```

Set `LOG_LEVEL=debug` to see all logs.

## Development

```bash
just build                 # Build binary to bin/android-studio-llm-proxy
just run                   # Build and run
just test                  # Run tests
just fmt                   # Format code
just container-build       # Build Docker image
just container-run         # Run Docker container
just compose-up            # Run with docker-compose
just apple-container-build # Build Apple Container image (requires `container` CLI)
just apple-container-run   # Run Apple Container image (requires `container` CLI)
```

### Architecture

```
cmd/proxy/main.go          — HTTP server, routing, env config
internal/config/            — Config file loading and model overrides
internal/sanitize/          — Request body sanitization per model family
internal/forward/           — Upstream forwarding (streaming + non-streaming)
internal/cache/             — Reasoning content cache for multi-turn conversations
```

## Contributing

Contributions are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

## License

MIT License - see [LICENSE](LICENSE) for details.
