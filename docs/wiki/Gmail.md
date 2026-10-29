# Gmail polling

Gmail is an optional event source. It polls locally through the [Google Workspace CLI](https://github.com/googleworkspace/cli) (`gws`); no inbound endpoint or Pub/Sub subscription is required. The CLI still needs Gmail API and OAuth credentials. Authenticate `gws` for the mailbox with permission to read messages before starting inletd. Check that `gws gmail users messages list --params '{"userId":"me","q":"label:github-events"}'` succeeds.

Use a dedicated mailbox or label and a dedicated state file. For example:

```json
{
  "provider": "gmail",
  "repository": "OWNER/REPO",
  "workdir": ".",
  "state_file": ".inletd/gmail-state.json",
  "poll_seconds": 120,
  "gmail": {
    "query": "label:github-events",
    "cli_path": "gws",
    "user_id": "me",
    "signing_key_env": "INLETD_GMAIL_SIGNING_KEY"
  },
  "rules": [
    {"event": "IssuesEvent", "actions": ["opened"], "command": ["codex", "exec", "-"], "prompt": "Handle {url}"}
  ]
}
```

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

The `payload` must have the fields required by the selected inletd rule, matching the GitHub event payload shape. Set the `X-Inletd-Signature-SHA256` email header to `sha256=` followed by the lowercase hex HMAC-SHA256 of the **exact UTF-8 bytes of the text/plain body**, using the shared signing key. The bridge should obtain the event type, repository, delivery ID, actor, and payload directly from the trusted GitHub Actions event context. Treat all event fields as untrusted input when constructing the email and do not let a pull request change the bridge workflow or access the signing key. The sender address is not used for authentication.

GitHub's native notification emails do not supply a guaranteed complete, structured webhook payload and are therefore not accepted as events. The bridge and its email delivery service are separate from inletd; this repository does not send event emails.

## Delivery and recovery

The source lists all messages matching the query across pages and fetches new message IDs. Successfully handled message IDs are retained in the state file without pruning, so an old message cannot dispatch again after restart. Keep the query narrow enough to avoid an excessively large mailbox; polling stops with an error above 1,000 pages (100,000 messages) and retries after backoff. Empty inboxes are valid. Invalid signatures, malformed bodies, and wrong-repository envelopes are recorded as rejected messages without running rules. CLI, quota, and authentication errors leave the batch uncommitted and retry after increasing delays (up to 15 minutes); authentication failures should be repaired in `gws` before retrying.

Email delivery can be delayed, duplicated, or lost. Inletd deduplicates by Gmail message ID, so a bridge that resends the same delivery as a **new email** must prevent duplicate sends itself. The state file grows with retained IDs. Agent execution has the same running-job reconciliation rules as other providers; see [Event handling and recovery](Event-Handling-and-Recovery.md).
