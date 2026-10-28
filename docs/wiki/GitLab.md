# GitLab projects

Copy [`inletd.gitlab.example.json`](../../inletd.gitlab.example.json) and set `repository` to the GitLab project path, including any subgroups. Set `provider` to `gitlab`. For self-managed GitLab, set `api_url` to the instance's API root, such as `https://gitlab.example.com/api/v4`. Keep a separate `state_file` for each provider, instance, and project. Existing GitHub configurations default to `github` and remain valid.

Create a GitLab personal, project, or group access token that can read the project and its issue/MR activity. The project Events API and issue/MR detail endpoints require access to the project. Use a token with the `read_api` scope (or `api` when needed by your instance). Set the environment variable named by `token_env` (default `GITLAB_TOKEN`) before starting Inletd. The token is not stored in the configuration or state file. The account running the agent needs separate permission to push branches and write merge request discussions; configure `glab auth login` or an equivalent GitLab API credential for that workflow.

Run `go run ./src -config inletd.gitlab.json`. On the first normal poll, Inletd records visible activity without dispatching it. Use `-replay-existing` only when intentionally processing the current feed on first run. `poll_seconds` has a 60-second minimum.

## Delivery and authorization

Inletd polls `GET /projects/:path/events`, up to three pages of 100 activities, and retrieves issue or MR details for notes. It handles opened/reopened MRs (`GitLabMergeRequestEvent`), opened/reopened issues (`GitLabIssueEvent`), and ordinary issue/MR notes (`GitLabNoteEvent`). The example only dispatches MR opens and notes containing exact commands. A note command must be the entire trimmed body, must target the configured resource type, and must come from a username in `allowed_actors`. System notes and issue/MR descriptions do not execute commands. The event and note authors must agree.

GitLab's [Events API](https://docs.gitlab.com/api/events/) does not include some MR discussion notes (`DiscussionNote`), so commands in those threads may not arrive. Use a top-level MR note for `/inletd review` or `/inletd fix`. Activity has [retention limits](https://docs.gitlab.com/user/profile/user_activity/#activity-time-period-limit); events lost beyond retention cannot be recovered by polling. This implementation does not accept webhooks. GitLab can also delay activity visibility. If a full 300-event window no longer contains the last cursor, Inletd records a gap; inspect activity and reissue missed commands before `-acknowledge-gap`.

The source respects GitLab `X-Next-Page` pagination and `Retry-After` response headers. On HTTP errors it does not advance durable state. Repeated delivery is deduplicated by IDs that include the GitLab API instance, project ID, and activity ID.

## Recovery

Pending jobs retain their event for replay after restart, even if it has left the feed. Running jobs need manual reconciliation because an agent may already have changed GitLab before a crash. Stop Inletd, inspect the issue/MR and working tree, then run `-resolve-event EVENT_ID -resolution completed` or `-resolution retry` as described in [Event handling and recovery](Event-Handling-and-Recovery.md). GitLab event IDs are prefixed with `gitlab:` and appear in logs and state. Do not share a state file between providers or projects.
