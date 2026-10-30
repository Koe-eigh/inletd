# Inletd

Run your existing AI coding agents locally, automatically triggered by remote events.

## The Idea

AI coding agents can automate software development tasks such as implementing issues, reviewing pull requests, and addressing review feedback. However, running these agents in the cloud can be costly, while running them locally often requires manual intervention.

**Inletd bridges this gap**

It listens for remote events such as GitHub events, and automatically triggers local AI coding agents to perform predefined tasks. By leveraging existing tools such as Codex CLI, Claude Code, and OpenCode, Inletd enables developers to automate their workflows using their own machines, without relying on cloud-hosted execution environments.

## Documentation

- [Getting started](https://github.com/Koe-eigh/inletd/wiki/Getting-Started)
- [Workflows](https://github.com/Koe-eigh/inletd/wiki/Workflows)
- [Event handling and recovery](https://github.com/Koe-eigh/inletd/wiki/Event-Handling-and-Recovery)
- [Limitations](https://github.com/Koe-eigh/inletd/wiki/Limitations)
- [GitLab projects](docs/wiki/GitLab.md)
- [Gmail polling](docs/wiki/Gmail.md)

## Gmail polling

Set `provider` to `gmail` to poll a Gmail mailbox through `gws` without an inbound port. Configure `gmail.query` (for example, `subject:inletd-event`), set a signing key of at least 32 bytes in `INLETD_GMAIL_SIGNING_KEY`, and use a dedicated `state_file`. Gmail polling accepts signed, structured GitHub event emails; ordinary GitHub notification emails do not contain a complete webhook payload. This repository includes an optional GitHub Actions email bridge. See [Gmail polling](docs/wiki/Gmail.md) for its setup and delivery limitations.

## Development checks

The Go files live in `src/` and keep each part of the polling workflow together: `config.go` loads and validates configuration, `github.go` polls GitHub through an `EventSource` driver, `state.go` defines state transitions and the `StateStore` port, `state_file.go` provides the JSON file driver, `rules.go` selects a rule and extracts event values, `runner.go` coordinates each poll through injected `EventSource`, `AgentRunner`, and `StateStore` interfaces, and `agent.go` provides the command-based driver that renders and runs the CLI. `main.go` wires the drivers into the CLI loop. They remain in one package; the consolidated JSON example stays at the repository root.

Run the same checks as GitHub Actions before opening a pull request:

```sh
go test ./...
go vet ./...
go build -o inletd ./src
test -z "$(gofmt -l .)"
```

The formatting command fails if any Go file needs `gofmt`. GitHub Actions runs these checks on pull requests and pushes to `main` without a token for the app or access to live services.
