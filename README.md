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
go run ./src -config inletd.json
```

Use `-once` to poll once and exit. On the first run, Inletd records the current events without running them. To intentionally process those events, use `-replay-existing -once` **before** the state file is initialized. Delete the state file only when you intentionally want to reset this history. An event with no matching rule is marked handled.

For events it discovers, Inletd uses **at-least-once dispatch with manual reconciliation**: a pending job can run again, but a running job is held until an operator checks its effects. Progress on an uncertain job therefore requires operator action. Inletd cannot guarantee exactly-once effects from an external agent. For a matching event, the state file records a `pending` job, then a `running` job before starting the agent, and finally a `completed` job after success. A crash, agent error, or failed completion write can leave `running` even if the agent created a commit or PR. On restart, Inletd skips that event and logs its ID and target URL. Stop the poller, inspect the external result and the `jobs` entry in the state file, then reconcile it:

```sh
go run ./src -config inletd.json -resolve-event EVENT_ID -resolution completed
# Only when the previous attempt had no effect, or its effects are safe to repeat:
go run ./src -config inletd.json -resolve-event EVENT_ID -resolution retry
```

`completed` records the event as handled; `retry` makes it pending for the next poll. Pending jobs retain the original event and run even after it leaves GitHub's recent events feed. Check the target issue or PR and your working tree before choosing `retry`. The event ID is the GitHub Events API ID, not the issue or PR number. Existing state files with only `seen` IDs remain valid. For jobs created by an earlier version without saved event data, retry requires the original event to still be in the feed; otherwise Inletd reports an error so the request can be reissued.

Each rule has a GitHub event type (`event`), optional payload action allowlist (`actions`), command argument array (`command`), and prompt (`prompt`). Supported replacements in the prompt and command arguments are `{repo}`, `{event}`, `{action}`, `{number}`, `{url}`, `{title}`, and `{requester}` for comment events. For issue and pull request events, Inletd builds the GitHub URL from the repository and number when the event omits it; `{title}` can be empty. `{prompt}` in a command argument inserts the rendered prompt; standard input always receives the prompt as well. Rules are checked in order; the first match runs. For example, the included configuration dispatches newly opened issues and pull requests to Codex. Edit the command array to use another installed CLI.

For a quick end-to-end test without invoking an AI agent, set a rule's command to `["sh", "-c", "cat > inletd-prompt.txt"]`, set `state_file` to a new path, and run with `-replay-existing -once` against a repository with a matching recent event. The prompt will appear in the configured working directory.

### Agent shutdown

Agent execution is supported on Linux and macOS. Inletd starts each agent command in a separate process group. When `agent_timeout_seconds` expires, or Inletd receives Ctrl+C or SIGTERM, it sends SIGTERM to the group, allows one second for cleanup, then sends SIGKILL and waits up to one more second for the agent to exit. A stopped command leaves its job in `running` state for manual reconciliation, since it may have made external changes before stopping. Other platforms reject agent execution because process tree cleanup is not implemented there. Abrupt termination of Inletd itself (such as SIGKILL or a machine crash) cannot run this cleanup.

### Review pull requests

Copy `inletd.pr-review.example.json` to the ignored `inletd.json`. Set `repository`, `workdir`, and `allowed_actors` for your repository. It reviews newly opened and reopened PRs. An allowed user can also comment `/inletd review` on a PR to request a review of its current version. The example asks the agent to check the current head SHA and its own prior reviews before posting, so repeated requests for the same commit do not produce another review.

Run Inletd once before posting the request to record existing events, then start polling:

```sh
export GITHUB_TOKEN="$(gh auth token)"
go run ./src -config inletd.json -once
go run ./src -config inletd.json
```

The command must match exactly, come from an allowed actor, and be posted on a PR rather than an issue. After pushing new commits, comment `/inletd review` again to review the new head. GitHub's [repository Events API](https://docs.github.com/en/rest/using-the-rest-api/github-event-types#pullrequestevent) does not list a `synchronize` action for `PullRequestEvent`. The request is detected through the Events API, so the same delivery delay applies. Stop polling with Ctrl+C.

### Implement an issue on demand

Copy `inletd.implement.example.json` to the ignored `inletd.json`. Set `repository`, `workdir`, and `allowed_actors` for your repository. Record existing events before starting the poller:

```sh
cp inletd.implement.example.json inletd.json
export GITHUB_TOKEN="$(gh auth token)"
go run ./src -config inletd.json -once
go run ./src -config inletd.json
```

An allowed user can then comment `/inletd implement` on an issue. The comment must contain exactly that command apart from surrounding whitespace. Comments on PRs and comments from other users do not start this workflow.

The example asks Codex to read the issue, work in the local checkout, run checks, and open a PR. The agent CLI must be installed and authenticated for GitHub writes through `gh`. Review the prompt and CLI permissions for your environment before starting Inletd. Each new command comment is a new request; an event already recorded in the state file is skipped on later polls. The example asks the agent to continue an existing PR for the same issue when possible.

For comment rules, `comment_target` can be `issue` or `pull_request`. If omitted, it defaults to `pull_request` so existing review configurations keep working. `comment_command` requires `allowed_actors` and matches an exact comment from one of those accounts.

### Address PR review feedback

Copy `inletd.fix-review.example.json` to the ignored `inletd.json`, then set `repository`, `workdir`, and `allowed_actors`. Record existing events with `-once` before starting the poller as shown above. An allowed user can comment `/inletd fix` on a PR to ask the agent to inspect its review threads, make justified changes, run checks, push to the PR branch, and reply to the reviewers. The command does not run from issue comments or other users.

Each new command comment is a separate request. A successfully handled event is recorded in the state file and skipped on later polls. If the agent command fails, reconcile its `running` job as described above before another attempt. Check the PR before posting the command again to avoid overlapping requests.

GitHub's [repository Events API](https://docs.github.com/en/rest/activity/events) exposes at most 300 events created within the last 30 days. GitHub says event delivery can lag by 30 seconds to 6 hours. Inletd saves the newest event ID from each completed poll as a cursor. When a full 300-event window no longer contains that cursor, Inletd saves `"gap": true` in the state file and reports a poll error on every subsequent poll. It still processes visible events, but requests outside the window cannot be recovered from this API. Inspect issues, PRs, and comments posted since the last good poll; reissue any missed requests, then clear the alert with:

```sh
go run ./src -config inletd.json -acknowledge-gap
```

Run that command while the poller is stopped. The command acknowledges possible loss; it does not recover missing events. Existing state files gain a cursor on their next successful poll, so that first poll has no overlap check. A short feed with no cursor overlap cannot prove whether events aged out after 30 days, and GitHub may omit or delay events even when the cursor overlaps. The initial poll also cannot establish what happened before it started. Inletd therefore guarantees dispatch only for events it actually discovers, not reliable delivery of every GitHub action. The MVP does not isolate concurrent changes made outside Inletd to the same working directory.

## Development checks

The Go files live in `src/` and keep each part of the polling workflow together: `config.go` loads and validates configuration, `github.go` polls GitHub through an `EventSource` driver, `state.go` defines state transitions and the `StateStore` port, `state_file.go` provides the JSON file driver, `rules.go` selects a rule and extracts event values, `runner.go` coordinates each poll through injected `EventSource`, `AgentRunner`, and `StateStore` interfaces, and `agent.go` provides the command-based driver that renders and runs the CLI. `main.go` wires the drivers into the CLI loop. They remain in one package; the JSON examples stay at the repository root.

Run the same checks as GitHub Actions before opening a pull request:

```sh
go test ./...
go vet ./...
go build -o inletd ./src
test -z "$(gofmt -l .)"
```

The formatting command fails if any Go file needs `gofmt`. GitHub Actions runs these checks on pull requests and pushes to `main` without a token for the app or access to live services.
