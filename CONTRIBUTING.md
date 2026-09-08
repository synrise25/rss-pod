# Contributing

Thanks for helping improve rss-pod.

## Development setup

1. Install the Go version declared in `go.mod`.
2. Copy `.env.example` to `.env` and `config.example.yaml` to `config.yaml`.
3. Replace placeholders with local development services. Keep local credentials
   and endpoints out of tracked files.
4. Run the validation suite:

   ```bash
   gofmt -w path/to/changed.go
   go vet ./...
   go test ./...
   docker build -t rss-pod:dev .
   ```

Admin authentication and episode visibility integration tests run automatically
in CI against PostgreSQL. Locally, set `RSS_POD_TEST_DATABASE_URL` to a disposable
PostgreSQL database before running `go test ./...` to include them. The tests
create and remove isolated schemas; never point this variable at production.

## Pull requests

- Keep changes focused and include tests for behavior changes.
- Update `config.example.yaml` when adding public configuration fields.
- Do not enable sample sources that can call paid or external services.
- Explain operational or migration impact in the pull request description.

