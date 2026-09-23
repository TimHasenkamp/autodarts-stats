BINARY   ?= autodarts-stats
# Architekturen fuers Paket. Standard nur amd64 (normaler PC).
# Fuer einen Raspberry Pi o.ae.:  make package ARCHES="amd64 arm64"
ARCHES   ?= amd64
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DIST      = server/internal/web/dist

.PHONY: all build build-web build-server build-agent dist-placeholder test test-extension extension-zip package clean docker run

all: build

## Frontend bauen (Ausgabe landet im Go-Embed-Verzeichnis)
## Der Adapter leert $(DIST), deshalb wird der Platzhalter danach neu angelegt.
## Ohne ihn scheitert go build in einem frischen Klon am //go:embed.
build-web:
	cd web && npm ci && npm run build
	@$(MAKE) --no-print-directory dist-placeholder

dist-placeholder:
	@mkdir -p $(DIST)
	@printf '%s\n' \
	  'Platzhalter. Hier landet das gebaute Dashboard (make build-web);' \
	  'es wird per //go:embed ins Binary gepackt. Die Datei muss im Repository' \
	  'bleiben, sonst scheitert go build an "pattern all:dist: no matching files found".' \
	  > $(DIST)/.gitkeep

## Nur das Go-Binary (Frontend muss vorher gebaut sein oder wird als Platzhalter eingebettet)
build-server: dist-placeholder
	cd server && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../$(BINARY) ./cmd/autodarts-stats

## Agent fuer den Board-Client (NFC-Leser). Mit PC/SC-Leser: make build-agent TAGS=pcsc
build-agent:
	cd server && CGO_ENABLED=$(if $(TAGS),1,0) go build -trimpath -ldflags "-s -w" $(if $(TAGS),-tags $(TAGS)) -o ../autodarts-stats-agent ./cmd/autodarts-stats-agent

## Komplettes Binary inkl. Frontend
build: build-web build-server build-agent

test:
	cd server && go test ./...
	cd web && npm run check

## Browser-Test der Extension (braucht Playwright, siehe tools/extension-e2e/README.md)
test-extension: build-server
	@test -d tools/extension-e2e/node_modules || { echo "Erst: cd tools/extension-e2e && npm install && npx playwright install chromium"; exit 1; }
	tools/extension-e2e/run.sh

## Extension als ZIP (zum Laden in Chrome/Firefox oder zum Weitergeben)
extension-zip:
	rm -f autodarts-stats-extension.zip
	cd extension && zip -r ../autodarts-stats-extension.zip . -x '*.DS_Store'

## Komplettpaket fuer Rechner ohne Go/Node (Transport per USB/Nextcloud)
## Standard: nur linux/amd64. Mehr Architekturen: make package ARCHES="amd64 arm64"
package: build-web extension-zip
	rm -rf dist-bin your-darts-paket.zip .pkg
	cd server && for a in $(ARCHES); do \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags "-s -w" -o ../dist-bin/autodarts-stats-linux-$$a ./cmd/autodarts-stats; \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags "-s -w" -o ../dist-bin/autodarts-stats-agent-linux-$$a ./cmd/autodarts-stats-agent; \
	done
	mkdir -p .pkg/your-darts
	rsync -a --exclude node_modules --exclude .svelte-kit --exclude '*.db*' \
	  README.md CLAUDE.md START-HIER.md Makefile Dockerfile .dockerignore .gitignore \
	  extension server web testdata deploy docs tools .pkg/your-darts/
	mkdir -p .pkg/your-darts/bin && cp dist-bin/* .pkg/your-darts/bin/ && chmod +x .pkg/your-darts/bin/*
	cp autodarts-stats-extension.zip .pkg/your-darts/
	find .pkg/your-darts/$(DIST) -type f ! -name .gitkeep -delete
	cd .pkg && zip -rq9 ../your-darts-paket.zip your-darts
	rm -rf .pkg
	@ls -lh your-darts-paket.zip

docker:
	docker build -t autodarts-stats:$(VERSION) .

run: build-server
	ADMIN_PASSWORD=admin ./$(BINARY)

clean:
	rm -rf $(BINARY) autodarts-stats-agent autodarts-stats-extension.zip your-darts-paket.zip dist-bin .pkg web/.svelte-kit
	find $(DIST) -mindepth 1 ! -name .gitkeep -delete
