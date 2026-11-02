# 3gpp-index 0.2.0

Static Linux indexer builds from source commit `f4e264986f37d43a4652857d638f9ac6dba9c755` on branch `work`.

- `3gpp-index_0.2.0_linux_amd64`: Linux x86_64.
- `3gpp-index_0.2.0_linux_arm64`: Linux ARM64.
- `SHA256SUMS`: SHA-256 checksums.

Go is not required at runtime. ETSI PDF import requires Poppler (`pdftotext`).

```sh
chmod +x 3gpp-index_0.2.0_linux_amd64
./3gpp-index_0.2.0_linux_amd64 --version
./3gpp-index_0.2.0_linux_amd64 --source all --db data/3gpp.db --resume --workers 8 --scrape-workers 8
```

Stop the MCP Docker container before changing its database. Restart it after successful completion. A failed indexing run leaves a partial database; rerun with `--resume`.
