# Gmail polling

Gmail is an optional event source. It polls locally through the [Google Workspace CLI](https://github.com/googleworkspace/cli) (`gws`); no inbound endpoint or Pub/Sub subscription is required. The CLI still needs Gmail API and OAuth credentials. Authenticate `gws` for the mailbox with permission to read messages before starting inletd. Check that `gws gmail users messages list --params '{"userId":"me","q":"label:github-events"}'` succeeds.

Use a dedicated mailbox or label and a dedicated state file. For example:

```json
{
  "provider": "gmail",
  "repository": "OWNER/REPO",
  "workdir": "../inletd-agent-checkout",
  "state_file": ".inletd/gmail-state.json",
  "poll_seconds": 120,
  "gmail": {
    "query": "label:github-events",
    "cli_path": "gws",
    "user_id": "me",
    "signing_key_env": "INLETD_GMAIL_SIGNING_KEY"
  },
  "rules": [
    {
      "event": "IssuesEvent", "actions": ["opened"],
      "execution": {"profile": "container", "image": "inletd-agent:local", "network": "bridge"},
      "env": {"GH_TOKEN": "INLETD_ACTION_GITHUB_TOKEN", "OPENAI_API_KEY": "INLETD_ACTION_OPENAI_API_KEY"},
      "command": ["codex", "exec", "-"], "prompt": "Handle {url}"
    }
  ]
}
```

Keep this config and `.inletd/` state directory outside the dedicated agent checkout. See [Agent execution and credentials](Agent-Security.md) for image setup and separate action tokens.

Set `INLETD_GMAIL_SIGNING_KEY` to the same high entropy secret in inletd's environment and in your trusted email bridge. The secret must contain at least 32 bytes. `gmail.query` is a Gmail search query and can include a label. `gmail.cli_path` defaults to `gws`; `gmail.user_id` defaults to `me`. Set `provider` to `github` or `gitlab` to disable Gmail polling. The usual `-once` and `-replay-existing` options apply. On the first poll, existing messages are recorded without dispatch unless `-replay-existing` is supplied.

## Email bridge format

A GitHub Actions email bridge must send one `text/plain` body containing a JSON object with these fields:

```json
{
  "delivery_id": "github-delivery-id",
  "event_type": "IssuesEvent",
  "repository": "OWNER/REPO",
  "actor": "github-login",
  "payload": {"action": "opened", "issue": {"number": 73, "title": "Example"}}
}
```

The `payload` must have the fields required by the selected inletd rule, matching the GitHub event payload shape. Set the `X-Inletd-Signature-SHA256` email header to `sha256=` followed by the lowercase hex HMAC-SHA256 of the **exact UTF-8 bytes of the text/plain body**, using the shared signing key. The bridge should obtain the event type, repository, delivery ID, actor, and payload directly from the trusted GitHub Actions event context. The signed delivery ID must be 1–128 ASCII letters, digits, hyphens, underscores, or periods. Treat all event fields as untrusted input when constructing the email and do not let a pull request change the bridge workflow or access the signing key. The sender address is not used for authentication.

GitHub's native notification emails do not supply a guaranteed complete, structured webhook payload and are therefore not accepted as events. A separate bridge must create the signed email; the optional GitHub Actions bridge below provides one way to do that.

## GitHub Actions bridge for this repository

The repository includes `.github/workflows/inletd-gmail.yml`, which sends signed emails for opened or reopened pull requests and newly created issue comments. It checks out only the trusted base repository code. The job is disabled until the repository variable `INLETD_GMAIL_ENABLED` is set to `true`.

Configure these GitHub Actions secrets before enabling the variable:

| Secret | Value |
| --- | --- |
| `INLETD_GMAIL_TO` | Mailbox address that inletd polls |
| `INLETD_SMTP_USER` | Gmail address used to send the event emails |
| `INLETD_SMTP_PASSWORD` | App password for the sender's Gmail account |
| `INLETD_GMAIL_SIGNING_KEY` | The same secret set in inletd's local environment, at least 32 bytes |

The sender needs [two-step verification and an app password](https://support.google.com/accounts/answer/185833). The bridge uses Gmail SMTP over TLS and a fixed subject beginning `inletd-event`. Set the local `gmail.query` to `subject:inletd-event`, and use a new state file when switching from the GitHub API source. Install and authenticate [`gws`](https://github.com/googleworkspace/cli) locally with Gmail read permission, then verify the mailbox with:

```sh
gws gmail users messages list --params '{"userId":"me","q":"subject:inletd-event"}'
```

Start inletd normally after the secrets and repository variable are set. Its first Gmail poll records existing messages without dispatching them. Keep the signing key out of the configuration file and repository. If delivery fails, inspect the `Deliver inletd events to Gmail` workflow run before retrying. Resending the same signed body is safe: it keeps the same delivery ID. Re-running the GitHub Actions job increases `GITHUB_RUN_ATTEMPT`, creating a new delivery ID and intentionally allowing another agent run.

## Delivery and recovery

The source uses a durable time checkpoint and a bounded Gmail search, rather than listing the entire mailbox. It adds `after:<epoch-seconds>` to the parenthesized configured query ([Gmail API filtering](https://developers.google.com/workspace/gmail/api/guides/filtering)). Normal polls start one hour before the last successful fetch's **start** time, allowing out-of-order arrivals and delayed search indexing within that overlap. All pages use the same query. A checkpoint advances only after every event in the batch has been handled durably, including empty batches; failures leave it unchanged. Pending and running jobs remain durable regardless of their age.

The first poll (including `-replay-existing`) and recovery after a long outage scan at most the last **seven days**. Older mail is intentionally excluded, even if it still matches `gmail.query`. Mail imported with an old internal timestamp, mail labeled after it has left the overlap, or indexing delays longer than one hour can be missed. After downtime longer than seven days, inspect the bridge workflow runs for missed requests and resend intended events as new mail. To backfill older requests, resend them with their original delivery IDs while those IDs are still retained; use new delivery IDs only when another run is intended. A fresh state file discards deduplication and should not be used for routine recovery. Search still has a safety limit of 1,000 pages within the bounded range; if exceeded, narrow the label/query and recover outstanding requests explicitly. No partial batch or checkpoint is committed on list/get or pagination errors.

Valid events are identified by `gmail:<lowercase-repository>:<delivery_id>`. Delivery IDs are unique within a repository and are case-sensitive. Handled delivery IDs and completed/rejected job records are retained for **30 days from first recording**, including after the original email is deleted. Fetch bookkeeping (Gmail message IDs, including invalid messages) expires after **seven days plus one hour**. Compaction runs before polling and on durable transitions. Storage therefore scales with traffic in these windows plus unresolved jobs, rather than all historical mail. Invalid signatures, malformed bodies, and wrong-repository envelopes never run rules.

Copies of a signed delivery, even as separate Gmail messages, are suppressed while its delivery ID is retained. **After 30 days, a fresh email containing that same signed body can execute again.** The envelope has no signed expiration time; this finite replay-protection window is a deliberate storage/security tradeoff. Keep signed mail and signing keys private, and rotate the bridge key if old signed bodies must no longer be accepted. Retention is not extended by receiving duplicate copies.

On upgrade, undated legacy IDs receive the upgrade time and remain protected for a full retention window before compaction; this conservatively delays the first reduction of old state. Legacy message-ID entries are translated to signed delivery IDs when their original email is fetched within the search window. If that email is deleted, outside the window, or already suppressed by mailbox bookkeeping, its signed delivery ID cannot be recovered; a later copy may run once. Preserve the existing state file during upgrades.

Empty inboxes are valid. CLI, quota, and authentication errors leave the batch uncommitted and retry after increasing delays (up to 15 minutes); repair authentication failures in `gws` before retrying. Agent execution has the same running-job reconciliation rules as other providers; see [Event handling and recovery](Event-Handling-and-Recovery.md).
