# Event handling and recovery

Inletd polls the GitHub repository Events API. On the first normal run, it records current events without dispatching them. An event with no matching rule is marked handled. For discovered matching events, Inletd uses **at-least-once dispatch with manual reconciliation**: a pending job may run again, while a running job is held until an operator checks its effects. External agents cannot guarantee exactly-once effects.

For a matching event, the state file records a `pending` job, then a `running` job before starting the agent, and finally a `completed` job after success. A crash, agent error, or failed completion write can leave `running` even if the agent created a commit or PR. On restart, Inletd skips that event and logs its ID and target URL.

Stop the poller. Inspect the target issue or PR, the working tree, and the `jobs` entry in the state file. Then choose one resolution:

```sh
go run ./src -config inletd.json -resolve-event EVENT_ID -resolution completed
# Only when the previous attempt had no effect, or its effects are safe to repeat:
go run ./src -config inletd.json -resolve-event EVENT_ID -resolution retry
```

`completed` marks the event handled; `retry` makes it pending for the next poll. The event ID is the GitHub Events API ID, not the issue or PR number. Pending jobs retain the original event and can run after it leaves GitHub's recent feed. Existing state files with only `seen` IDs remain valid. For jobs created by an earlier version without saved event data, retry requires the original event still to be in the feed; otherwise Inletd reports an error so the request can be reissued.

## Event gaps

GitHub's [repository Events API](https://docs.github.com/en/rest/activity/events) exposes at most 300 events created within the last 30 days and says delivery may lag by 30 seconds to 6 hours. Inletd saves the newest event ID from each completed poll as a cursor. If a full 300-event window no longer contains that cursor, Inletd saves `"gap": true` in the state file and reports a poll error on every subsequent poll. It still processes visible events, but cannot recover requests outside that window from this API.

Inspect issues, PRs, and comments since the last good poll. Reissue any missed requests. Then, while polling is stopped, acknowledge the possible loss:

```sh
go run ./src -config inletd.json -acknowledge-gap
```

Acknowledgment clears the alert; it does not recover missing events. Existing state files gain a cursor on their next successful poll, so that first poll has no overlap check. See [Limitations](Limitations) for other delivery constraints.
