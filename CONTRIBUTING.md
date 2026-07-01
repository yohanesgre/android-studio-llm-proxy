# Contributing to Android Studio LLM Proxy

Thank you for your interest in contributing! This guide will help you get started.

## Prerequisites

- **Go 1.22+** — [Download Go](https://go.dev/dl/)
- **just** — [Installation guide](https://github.com/casey/just#installation)

## Setup

```bash
git clone https://github.com/yohanesgre/android-studio-llm-proxy.git
cd android-studio-llm-proxy
just build
just test
```

## Development Workflow

1. **Fork** the repository on GitHub
2. **Clone** your fork locally
3. **Create a branch** for your changes: `git checkout -b feature/your-feature-name`
4. **Make your changes** and test them
5. **Run tests**: `just test`
6. **Run linter**: `go vet ./...`
7. **Run race detector**: `go test -race ./...`
8. **Format code**: `just fmt`
9. **Commit** with a clear message
10. **Push** to your fork and **create a Pull Request**

## Code Style

- Follow standard Go conventions
- Use `just fmt` to format code before committing
- Run `go vet ./...` to catch common issues
- Ensure all tests pass with `go test -race ./...`
- Add tests for new functionality
- Keep functions focused and well-documented

## Adding a New Model Family

To add support for a new model family:

1. **Update `internal/sanitize/sanitize.go`**:
   - Add a new `family` constant (e.g., `familyNewModel`)
   - Add case(s) to `detectFamily()` to recognize model names
   - Add case to `applyRules()` with model-specific sanitization logic

2. **Add tests** in `internal/sanitize/sanitize_test.go`:
   - Test model detection
   - Test sanitization rules
   - Test edge cases

3. **Update documentation**:
   - Add to "Supported Models" section in README.md
   - Update sanitization rules section

## Pull Request Process

1. Ensure your PR description clearly describes the problem and solution
2. Include relevant issue numbers if applicable
3. Make sure all CI checks pass
4. Keep PRs focused — one feature/fix per PR
5. Be responsive to review comments

## Reporting Issues

- Use the GitHub issue tracker
- Include steps to reproduce the issue
- Provide logs if relevant (set `LOG_LEVEL=debug`)
- Mention your proxy version (`./bin/proxy -version`)

## Questions?

Feel free to open an issue for questions or discussions.

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
