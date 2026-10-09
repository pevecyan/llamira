VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
NVM     := . $$HOME/.nvm/nvm.sh >/dev/null 2>&1 && nvm use 22 >/dev/null 2>&1;
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: build web go test dev release clean

# web must finish before go embeds web/dist (sequential even under make -j)
build:
	$(MAKE) web
	$(MAKE) go

web:
	cd web && ($(NVM) npm ci --no-audit --no-fund && npm run build)
	touch web/dist/.keep

go:
	$(GOBUILD) -o bin/llamira ./cmd/llamira

test:
	go vet ./... && go test ./...
	cd web && ($(NVM) npm run typecheck)

# Go side on :7070 (connect to the default host), Vite with hot reload on :5173.
dev:
	@echo "UI: http://localhost:5173"
	go run ./cmd/llamira connect --no-browser & \
	cd web && ($(NVM) npm run dev); kill %1

release:
	$(MAKE) web
	GOOS=linux  GOARCH=amd64 $(GOBUILD) -o bin/llamira-linux-amd64 ./cmd/llamira
	GOOS=linux  GOARCH=arm64 $(GOBUILD) -o bin/llamira-linux-arm64 ./cmd/llamira
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o bin/llamira-darwin-arm64 ./cmd/llamira
	GOOS=windows GOARCH=amd64 $(GOBUILD) -o bin/llamira-windows-amd64.exe ./cmd/llamira

clean:
	rm -rf bin web/dist/assets web/dist/index.html
