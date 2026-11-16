VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
SWAG    := go run github.com/swaggo/swag/cmd/swag@v1.16.6

.PHONY: all build web swagger test vet docker clean

all: web build

# Build the web interface and place it where the server embeds it from.
web:
	cd web && npm ci && npm run build
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -r web/dist/. internal/webui/dist/

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/hls-monitor-server ./cmd/hls-monitor-server
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/hls-monitor-agent ./cmd/hls-monitor-agent

# Regenerate the API specification from the annotations in internal/api.
swagger:
	$(SWAG) init -g internal/api/swagger.go -o docs --parseInternal --parseDependency --outputTypes go,json,yaml

test:
	go test ./...
	cd web && npm run typecheck && npm run i18n

vet:
	go vet ./...

docker:
	docker compose -f deploy/docker-compose.yml -f deploy/docker-compose.build.yml --profile agent build

clean:
	rm -rf bin web/dist
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
