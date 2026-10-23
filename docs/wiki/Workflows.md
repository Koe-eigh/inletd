# Workflows

All rules below are in the single [inletd.example.json](https://github.com/Koe-eigh/inletd/blob/main/inletd.example.json). Copy it to `inletd.json` and follow [Getting started](Getting-Started.md) to set `repository`, `workdir`, and `allowed_actors` and start the poller. The command comments must match exactly apart from surrounding whitespace. Each new matching comment is a separate request; an event already recorded in state is skipped on later polls.

## Automatic PR review

The `PullRequestEvent` rule reviews newly opened and reopened PRs. Its prompt asks the agent to check the current head SHA and its own prior reviews before posting, so repeated requests for the same commit do not create another review. The rule has no `allowed_actors` restriction; remove it if automatic reviews are not wanted.

## Requested PR review

An allowed user can comment `/inletd review` on a PR to review its current version. After pushing new commits, comment again to review the new head. Issue comments and comments from other users do not trigger this rule. The rule uses `comment_target: "pull_request"` and `allowed_actors`; keep the allowlist current. GitHub's [repository Events API](https://docs.github.com/en/rest/using-the-rest-api/github-event-types#pullrequestevent) does not list a `synchronize` action for `PullRequestEvent`, so a new command comment is needed after a push.

## Implement an issue on demand

An allowed user can comment `/inletd implement` on an issue. PR comments and comments from other users do not trigger this rule. The rule uses `comment_target: "issue"`. Its prompt asks the agent to read the issue, run checks, and open or continue a PR. The agent CLI must be installed and authenticated for GitHub writes through `gh`. Review its CLI permissions before starting Inletd.

## Address PR review feedback

An allowed user can comment `/inletd fix` on a PR to ask the agent to inspect review threads, make justified changes, run checks, push to the PR branch, and reply to reviewers. Issue comments and comments from other users do not trigger this rule. Check the PR before posting the command again to avoid overlapping requests. If the agent fails, reconcile its `running` job before another attempt.

For comment rules, `comment_command` requires `allowed_actors`. `comment_target` may be `issue` or `pull_request`; if omitted, it defaults to `pull_request` for compatibility with older review configurations. Rules run in order and the first match wins. The example orders the automatic PR event first, followed by three distinct command rules.

## Actor authorization

For every rule, `allowed_actors` restricts execution to the listed GitHub logins (case insensitive), even when `comment_command` is absent. If `allowed_actors` is omitted or empty, the rule is unrestricted. A restricted rule never runs when the actor cannot be identified.

| Event type | Actor used for authorization |
| --- | --- |
| `IssueCommentEvent` | The repository Events API's top-level `actor.login` (the user who triggered the event). `payload.comment.user.login` must also be present and identify the same user. |
| `IssuesEvent` | The top-level `actor.login`, which identifies who performed the issue action; `payload.issue.user.login` identifies the issue creator and is not used. |
| `PullRequestEvent` | The top-level `actor.login`, which identifies who performed the PR action; `payload.pull_request.user.login` identifies the PR creator and is not used. |
| `PullRequestReviewEvent` | The top-level `actor.login`, which identifies who performed the review action; the PR creator is not used. |
| Other repository event types | The same top-level `actor.login`, if a rule is configured for that type. |

GitHub defines the top-level actor as the user who triggered a repository event in its [Events API documentation](https://docs.github.com/en/rest/using-the-rest-api/github-event-types#event-object-common-properties). A missing top-level actor, or a missing or conflicting comment author on a restricted comment rule, makes the rule ineligible. Public rules remain eligible without actor data.
