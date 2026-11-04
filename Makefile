GO ?= go
PYTHON ?= .venv/bin/python
.PHONY: build setup serve test index
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
