BUILD_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.buildVersion=$(BUILD_VERSION)
BIN := bin/snowbrawl-server

.PHONY: build run dev test bench lint tidy docker clean

build: ## Собрать бинарник с версией из git
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/snowbrawl-server

run: build ## Запустить с встроенной статикой
	SNOWBRAWL_ADMIN_TOKEN=dev ./$(BIN) --log-pretty

dev: ## Запуск для разработки: статика с диска, подробные логи
	go run ./cmd/snowbrawl-server --web-dir web --log-pretty --log-level debug --admin-token dev

test: ## Тесты
	go test -race ./...

bench: ## Бенчмарк goja: 17 матчей 4×4 при 20 тиках/с
	go test ./internal/sim/ -run xxx -bench FullLoad -benchtime 5x

# golangci-lint должен быть собран тем же (или более новым) Go, что и проект,
# поэтому запускаем его через go run тулчейном из go.mod.
GOLANGCI := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

lint: ## golangci-lint
	$(GOLANGCI) run ./...

tidy:
	go mod tidy

docker: ## Собрать образ локально
	docker build -f deploy/Dockerfile --build-arg BUILD_VERSION=$(BUILD_VERSION) -t snowbrawl:$(BUILD_VERSION) .

clean:
	rm -rf bin
