# 3gpp-index 0.2.1

Source commit: `124ea92d1970bcef283cb5957049fb4ee60a76ed` on branch `work`.

Fixes legacy GSM documents with `0 Scope`, `0.1 Scope` and `1.1 Scope`; preserves parent sections and original numbering; recognizes GSM cover designations and replaces old ETSI aliases after successful import. All 25 reported PDFs parsed and stored successfully in SQLite.

Static Linux amd64/arm64 builds. No Go runtime is required. PDF extraction requires Poppler (`pdftotext`).

Download:
- [Linux amd64](https://github.com/vemneyy/3gpp-mcp/raw/refs/heads/binaries-0.2.1/3gpp-index_0.2.1_linux_amd64)
- [Linux arm64](https://github.com/vemneyy/3gpp-mcp/raw/refs/heads/binaries-0.2.1/3gpp-index_0.2.1_linux_arm64)
- [SHA256SUMS](https://github.com/vemneyy/3gpp-mcp/raw/refs/heads/binaries-0.2.1/SHA256SUMS)

Update only the host indexer. Rebuilding the MCP Docker image is unnecessary. Stop any old indexing process and the existing MCP container, run the new indexer with `--resume`, then start the same container only after successful indexing. Existing database/schema and container port remain compatible.

```sh
chmod +x 3gpp-index_0.2.1_linux_amd64
./3gpp-index_0.2.1_linux_amd64 --version
./3gpp-index_0.2.1_linux_amd64 --source all --db data/3gpp.db --resume --workers 8 --scrape-workers 8
```

Verification: `go test -short -race ./...`, `go vet ./...`, golangci-lint: 0 issues; actual 25-document PDF extraction corpus plus SQLite insertion. Docker was not tested per request.
