# Getting started

Inletd requires Go 1.23 or later, a local checkout of the target repository, and an installed AI agent CLI. The example runs Codex CLI. Install and authenticate `gh` if you want the agent to read or write GitHub issues and PRs. Set `GITHUB_TOKEN` for private repositories and higher API rate limits; an existing `gh` login can provide the token.

From the Inletd repository root:

```sh
cp inletd.example.json inletd.json
```

Edit `inletd.json`:

1. Set `repository` to `OWNER/REPO` for the GitHub repository to poll.
2. Set `workdir` to the local checkout where the agent should run.
3. Replace every `YOUR_LOGIN` in `allowed_actors` with the GitHub login or logins allowed to request work. The automatic PR review rule does not use this allowlist.
4. Review the rules, prompts, command arrays, state file, and agent permissions. Remove any workflow you do not want to enable. Rules run in file order, and the first match wins.

The config's `workdir` and `state_file` paths are resolved relative to the config file. The default example keeps state in `.inletd/state.json`. Use a separate state path if testing a different event history.

Start by recording the currently visible events without dispatching them, then poll continuously:

```sh
export GITHUB_TOKEN="$(gh auth token)"
go run ./src -config inletd.json -once
go run ./src -config inletd.json
```

Stop polling with Ctrl+C. `-once` polls once and exits. On an uninitialized state file, Inletd records current events without running agents. To intentionally process those existing events, run `go run ./src -config inletd.json -replay-existing -once` **before** the first normal poll. Delete the state file only when you intentionally want to reset event history.

Each rule specifies a GitHub event type (`event`), optional payload action allowlist (`actions`), command argument array (`command`), and prompt (`prompt`). Commands run sequentially in the configured working directory, without a shell unless you explicitly configure one. Inletd passes the rendered prompt on standard input, so `codex exec --approve-for-me -` receives issue text without putting it in a shell command. Edit the command array to use another installed CLI.

Prompt and command arguments support `{repo}`, `{event}`, `{action}`, `{number}`, `{url}`, `{title}`, and `{requester}` for comment events. `{prompt}` in a command argument inserts the rendered prompt; standard input always receives it. For issue and PR events, Inletd constructs the GitHub URL from the repository and number when the event omits it; `{title}` can be empty.

For a quick test without an AI agent, set one rule's command to `["sh", "-c", "cat > inletd-prompt.txt"]`, set `state_file` to a new path, and run with `-replay-existing -once` against a repository with a matching recent event. The prompt appears in the configured working directory.

See [Workflows](Workflows) for the included rules and [Event handling and recovery](Event-Handling-and-Recovery) before retrying a failed job.
