# Agent execution and credentials

Event fields (including titles, URLs, comments, and linked issue/PR descriptions) and repository files are untrusted data. A prompt telling an AI to ignore hostile instructions is not a security boundary. Authorize the operation in administrator-owned rules, restrict event actors where appropriate, and enforce resource access outside the agent. Do not grant permissions merely because an event or repository instruction requests them.

## Migration

Rules now default to the `container` execution profile. Existing rules without an image fail configuration validation; there is no automatic fallback to host execution. Choose a profile for **every** rule:

```json
"execution": {
  "profile": "container",
  "image": "inletd-agent:local",
  "network": "none"
}
```

Build or pull a trusted Linux image containing your agent CLI, Git, and any tools it needs **before** starting Inletd. Prefer an immutable image digest for deployed configurations. Inletd uses `--pull=never` and overrides the image entrypoint with the configured command. Configure your CLI for noninteractive operation with its own permission controls; Inletd provides stdin for the prompt and no TTY for interactive approvals. The example retains its existing Codex command. Its image name is a local placeholder you must build, not a published image.

Docker must be installed and its local daemon reachable at `unix:///var/run/docker.sock`. Every run and cleanup command explicitly uses that endpoint and the same fresh, empty Docker configuration directory outside the checkout. Saved contexts, credential helpers, and `proxies.default` from the host Docker configuration are not loaded. `DOCKER_*` action variable names are rejected for container rules so they cannot change Docker client behavior. On macOS, make the local Docker socket available. On platforms other than Linux and macOS, agent execution still fails closed because process cleanup is unsupported.

The container uses a non-root numeric UID/GID matching the daemon user (65534:65534 if the daemon is root), a read-only root filesystem, dropped capabilities, no privilege escalation, a 256-process limit, and a 256 MiB temporary `/tmp` with `HOME=/tmp`. The configured `workdir` is the only host bind mount, writable at `/workspace`. Ensure its permissions allow the selected user to write. No home directory, SSH socket, Docker socket, or host CLI configuration is mounted. Image contents and any image-declared volumes remain trusted administrator inputs.

Networking defaults to `none`. Set `network` to `bridge` explicitly for remote AI APIs, fetching dependencies, and GitHub/GitLab access, as the bundled examples do. Bridge networking permits outbound access, including reachable LAN services; it is **not** a domain allowlist. Apply host firewall/proxy policy when narrower egress is needed. `host`, custom networks, extra mounts, and privileged options are not exposed by this profile. Docker flags are documented in the [Docker run reference](https://docs.docker.com/reference/cli/docker/container/run/).

Use a dedicated disposable checkout containing only files the agent may read/write. Keep daemon configuration, state, secrets, and credential-bearing Git remote URLs outside it. Config loading and each container dispatch reject config/state/lock paths that are inside or traverse the checkout, recording every directory entry visited during each symlink expansion, including multi-link chains that enter and leave the checkout and state files that do not exist yet. The bundled layouts keep configuration and `.inletd/` in the Inletd directory and mount only a sibling `../inletd-agent-checkout`. The temporary Docker configuration directory must also be outside the checkout; a `TMPDIR` inside it is rejected. A Git worktree whose `.git` file points outside the mount cannot access that external Git directory; use an independent clone instead. Containers do not protect files inside the mounted checkout, grant approval for executing arbitrary repository code, or limit the powers of forwarded action tokens. Treat output diffs and generated artifacts as untrusted until reviewed. Normal completion, failure, and timeout all attempt forced container removal using a fresh bounded context. A daemon or Docker crash may leave a container behind; inspect `docker ps -a --filter name=inletd-` before restarting/retrying work.

To explicitly retain direct execution on the daemon's host:

```json
"execution": {"profile": "unrestricted"}
```

This retains environment filtering, but the agent has the daemon user's filesystem, network, and local credential access, including credentials discoverable in home directories. Use a dedicated OS account with limited permissions if selecting this profile. It is not a sandbox.

## Separate ingest and action credentials

Children no longer inherit the daemon environment. Host commands receive only `PATH`, `LANG`, `LC_ALL`, `TERM`, and `TZ`, plus explicitly configured rule variables. The container's environment comes from the trusted image, `HOME=/tmp`, and explicit rule variables; host PATH and locale are not forwarded into the image. Host `HOME`, `SSH_AUTH_SOCK`, Git configuration, proxies, cloud credentials, and CLI auth settings are not inherited automatically.

Each rule's `env` maps the **child variable name** to an **environment variable holding a separate action credential** in the daemon:

```json
"env": {
  "GH_TOKEN": "INLETD_ACTION_GITHUB_TOKEN",
  "OPENAI_API_KEY": "INLETD_ACTION_OPENAI_API_KEY"
}
```

For GitLab, use `"GITLAB_TOKEN": "INLETD_ACTION_GITLAB_TOKEN"`. These values are not stored in configuration or put in command arguments. Missing or empty mapped variables fail dispatch before launching the command. Only configure credentials that particular rule needs. Never put secret values or event placeholders in `env`; its values are variable names, not templates.

Forwarding from `GITHUB_TOKEN`, `GITLAB_TOKEN`, `INLETD_GMAIL_SIGNING_KEY`, the configured `token_env`, or `gmail.signing_key_env` is rejected even when renamed. A separate action source may map to a CLI's conventional `GITHUB_TOKEN` or `GITLAB_TOKEN` name. Use different tokens with different scopes, not duplicate copies of the same ingest token under another name. Inletd cannot detect credentials embedded in files/images or aliased by an administrator.

- GitHub ingest: use a repository-restricted fine-grained token with read-only permissions required for the Events API, or unauthenticated access for public repositories. Do not grant ingest write permissions. See [repository events](https://docs.github.com/en/rest/activity/events#list-repository-events).
- GitHub actions: restrict the token to the target repository. A review-only rule needs pull request write permission to submit reviews and read permissions for its inputs; grant contents write only to rules that must push branches, and issues write only when they must modify issues. Use a separate review token and implementation token when their permissions differ. See [fine-grained token permissions](https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens).
- GitLab ingest: prefer a project access token with `read_api` and the lowest role that can read required project events. Action credentials should be project-scoped; grant `write_repository` for Git pushes and `api` only when MR/discussion writes require it. GitLab's `api` scope is broad, so minimize the token's project membership/role. See [GitLab token scopes](https://docs.gitlab.com/user/profile/personal_access_tokens/#personal-access-token-scopes).
- Gmail signing keys authenticate incoming envelopes and must never be agent credentials. Keep Gmail/gws login files outside the agent checkout and image.
