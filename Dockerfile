# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=0.1.2" -o proxy ./cmd/proxy

FROM alpine:3.21

RUN apk add --no-cache ca-certificates && \
    adduser -D -h /home/proxy -u 1000 proxy && \
    mkdir -p /home/proxy/.config/android-studio-llm-proxy && \
    chown -R proxy:proxy /home/proxy/.config

WORKDIR /home/proxy
COPY --from=builder /app/proxy /usr/local/bin/proxy

EXPOSE 9999

# Set LOG_LEVEL to debug, info, warn, or error.
ENV LOG_LEVEL=${LOG_LEVEL:-info}

USER proxy

ENTRYPOINT ["proxy"]
