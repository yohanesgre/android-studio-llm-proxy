VERSION := "0.1.2"

build:
  go build -ldflags "-X main.version={{VERSION}}" -o bin/android-studio-llm-proxy ./cmd/proxy

run: build
  ./bin/android-studio-llm-proxy

test:
  go test ./...

fmt:
  go fmt ./...

vet:
  go vet ./...

race:
  go test -race ./...

clean:
  rm -rf bin/

# Build container image (Docker or Podman)
container-build:
  docker build -t android-studio-llm-proxy:{{VERSION}} .

# Run container with default ports
container-run:
  docker run --rm -p 9999:9999 \
    -v ${HOME}/.config/android-studio-llm-proxy:/home/proxy/.config/android-studio-llm-proxy \
    android-studio-llm-proxy:{{VERSION}}

# Build Apple Container image (requires the `container` CLI from apple/container)
apple-container-build:
  container build -f Containerfile -t android-studio-llm-proxy:{{VERSION}} .

# Run Apple Container image with default ports
apple-container-run:
  container delete -f android-studio-llm-proxy || true
  container run --rm -p 9999:9999 --name android-studio-llm-proxy \
    -v ${HOME}/.config/android-studio-llm-proxy:/home/proxy/.config/android-studio-llm-proxy \
    android-studio-llm-proxy:{{VERSION}} &

# Run with docker-compose
compose-up:
  docker compose up --build -d

compose-down:
  docker compose down
