# Security Policy

## Supported Versions

Only the latest release is supported with security updates.

| Version | Supported          |
| ------- | ------------------ |
| latest  | :white_check_mark: |
| older   | :x:                |

## Reporting a Vulnerability

If you discover a security issue in this project, please email the maintainer directly instead of opening a public issue:

- **yohanesgre** — via GitHub (https://github.com/yohanesgre/android-studio-llm-proxy)

Please include:
- A description of the vulnerability
- Steps to reproduce it
- The version/commit where you observed it
- Any suggested fix or mitigation

You can expect an initial response within 7 days.

## Security Notes

- The proxy runs locally and forwards requests to a configured upstream LLM API.
- API keys are **not** read from environment variables by the proxy. They are passed through from the Android Studio client via the `Authorization` header.
- Do not commit real API keys, cookies, or other secrets to the repository.
- When running in a container, bind-mount only the config directory and avoid mounting sensitive host paths.
