# Inletd

Run your existing AI coding agents locally, automatically triggered by remote events.

## The Idea

AI coding agents can automate software development tasks such as implementing issues, reviewing pull requests, and addressing review feedback. However, running these agents in the cloud can be costly, while running them locally often requires manual intervention.

**Inletd bridges this gap**

It listens for remote events such as GitHub events, and automatically triggers local AI coding agents to perform predefined tasks. By leveraging existing tools such as Codex CLI, Claude Code, and OpenCode, Inletd enables developers to automate their workflows using their own machines, without relying on cloud-hosted execution environments.

## MVP: GitHub events to local agents

Inletd polls the GitHub repository events API and runs the first matching rule for each new event. Commands run sequentially in the configured local working directory. It passes the rendered prompt on standard input, so `codex exec --approve-for-me -` works without putting issue text in a shell command. No shell is involved unless you explicitly configure one.

Requirements: Go 1.23 or later, a local checkout of the target repository, and an installed agent CLI. Set `GITHUB_TOKEN` for private repositories and higher API rate limits. An existing `gh` login can provide it.

```sh
cp inletd.example.json inletd.json
# Edit repository, workdir, and rules in inletd.json.
export GITHUB_TOKEN="$(gh auth token)"
go run . -config inletd.json
```

Use `-once` to poll once and exit. On the first run, Inletd records the current events without running them. To intentionally process those events, use `-replay-existing -once` **before** the state file is initialized. Delete the state file only when you intentionally want to reset this history. The state file is written after every successfully handled event. A failed agent exits the current poll without marking its event as handled, so it is retried on the next poll. An event with no matching rule is marked handled.

Each rule has a GitHub event type (`event`), optional payload action allowlist (`actions`), command argument array (`command`), and prompt (`prompt`). Supported replacements in the prompt and command arguments are `{repo}`, `{event}`, `{action}`, `{number}`, `{url}`, `{title}`, and `{requester}` for comment events. For issue and pull request events, Inletd builds the GitHub URL from the repository and number when the event omits it; `{title}` can be empty. `{prompt}` in a command argument inserts the rendered prompt; standard input always receives the prompt as well. Rules are checked in order; the first match runs. For example, the included configuration dispatches newly opened issues and pull requests to Codex. Edit the command array to use another installed CLI.

For a quick end-to-end test without invoking an AI agent, set a rule's command to `["sh", "-c", "cat > inletd-prompt.txt"]`, set `state_file` to a new path, and run with `-replay-existing -once` against a repository with a matching recent event. The prompt will appear in the configured working directory.

### Review pull requests

Copy `inletd.pr-review.example.json` to the ignored `inletd.json`. Set `repository`, `workdir`, and `allowed_actors` for your repository. It reviews newly opened and reopened PRs. An allowed user can also comment `/inletd review` on a PR to request a review of its current version. The example asks the agent to check the current head SHA and its own prior reviews before posting, so repeated requests for the same commit do not produce another review.

Run Inletd once before posting the request to record existing events, then start polling:

```sh
export GITHUB_TOKEN="$(gh auth token)"
go run . -config inletd.json -once
go run . -config inletd.json
```

The command must match exactly, come from an allowed actor, and be posted on a PR rather than an issue. After pushing new commits, comment `/inletd review` again to review the new head. GitHub's [repository Events API](https://docs.github.com/en/rest/using-the-rest-api/github-event-types#pullrequestevent) does not list a `synchronize` action for `PullRequestEvent`. The request is detected through the Events API, so the same delivery delay applies. Stop polling with Ctrl+C.

### Implement an issue on demand

Copy `inletd.implement.example.json` to the ignored `inletd.json`. Set `repository`, `workdir`, and `allowed_actors` for your repository. Record existing events before starting the poller:

```sh
cp inletd.implement.example.json inletd.json
export GITHUB_TOKEN="$(gh auth token)"
go run . -config inletd.json -once
go run . -config inletd.json
```

An allowed user can then comment `/inletd implement` on an issue. The comment must contain exactly that command apart from surrounding whitespace. Comments on PRs and comments from other users do not start this workflow.

The example asks Codex to read the issue, work in the local checkout, run checks, and open a PR. The agent CLI must be installed and authenticated for GitHub writes through `gh`. Review the prompt and CLI permissions for your environment before starting Inletd. Each new command comment is a new request; an event already recorded in the state file is skipped on later polls. The example asks the agent to continue an existing PR for the same issue when possible.

For comment rules, `comment_target` can be `issue` or `pull_request`. If omitted, it defaults to `pull_request` so existing review configurations keep working. `comment_command` requires `allowed_actors` and matches an exact comment from one of those accounts.

### Address PR review feedback

Copy `inletd.fix-review.example.json` to the ignored `inletd.json`, then set `repository`, `workdir`, and `allowed_actors`. Record existing events with `-once` before starting the poller as shown above. An allowed user can comment `/inletd fix` on a PR to ask the agent to inspect its review threads, make justified changes, run checks, push to the PR branch, and reply to the reviewers. The command does not run from issue comments or other users.

Each new command comment is a separate request. A successfully handled event is recorded in the state file and skipped on later polls. If the agent command fails, the event remains pending and is retried on the next poll. Check the PR before posting the command again to avoid overlapping requests.

GitHub's repository events API is eventually consistent and returns at most the latest 300 events from the last 30 days. Inletd may miss events if the machine is offline while more than 300 events arrive, or if GitHub does not expose an event in this feed. It logs a warning when it reaches the 300-event limit. The MVP does not isolate concurrent changes made outside Inletd to the same working directory.

## Development checks

The Go files keep each part of the polling workflow together: `config.go` loads and validates configuration, `github.go` fetches events, `state.go` persists handled IDs, `rules.go` selects a rule and extracts event values, `agent.go` renders and runs the command, and `runner.go` coordinates each poll. `main.go` contains the CLI loop. They remain in one package so the JSON examples and executable interface stay the same.

Run the same checks as GitHub Actions before opening a pull request:

```sh
go test ./...
go vet ./...
go build ./...
test -z "$(gofmt -l .)"
```

The formatting command fails if any Go file needs `gofmt`. GitHub Actions runs these checks on pull requests and pushes to `main` without a token for the app or access to live services.
