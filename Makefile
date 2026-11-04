GO ?= go
PYTHON ?= .venv/bin/python
.PHONY: build build-amd64 docker-build docker-up setup serve test index
build:
	$(GO) build -trimpath -o bin/etsi-indexer ./indexer
setup:
	python3 -m venv .venv
	.venv/bin/pip install -r requirements.lock
serve:
	$(PYTHON) -m uvicorn backend.app:app --host 127.0.0.1 --port 8000
index: build
	./bin/etsi-indexer --db data/explorer.db
test:
	$(GO) test -race ./...
	$(GO) vet ./...
	$(PYTHON) -m pytest -q

build-amd64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 $(GO) build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o dist/etsi-indexer-linux-amd64 ./indexer
	cd dist && sha256sum etsi-indexer-linux-amd64 > SHA256SUMS

docker-build:
	docker compose build

docker-up:
	docker compose up -d
