# Repository guidance

## Local project context

If `.private-context/index.md` exists, read it before planning, reviewing, or
modifying this project. The directory is intentionally untracked and contains
maintainer-only background. Follow its routing instructions and read only the
documents relevant to the current task.

## Public/private boundary

- `config.example.yaml` is the publishable configuration example.
- `config.yaml`, `.env`, `.data/`, and `.private-context/` are local-only.
- Never copy private endpoints, credentials, generated media, or maintainer
  notes from local-only files into tracked files.
- Keep all examples safe to publish and disabled by default when they can call
  paid or external services.

## Validation

- Run `gofmt` on changed Go files.
- Run `go test ./...` for code or configuration changes.
- Run `go vet ./...` when changing Go behavior.
- Build the Docker image when changing the Dockerfile, embedded web assets,
  configuration example, or release workflow.

