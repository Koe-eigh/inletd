# Limitations

## Event delivery

Inletd guarantees dispatch only for events it actually discovers. The GitHub Events API exposes at most 300 events from the last 30 days and may delay delivery by 30 seconds to 6 hours. A full 300-event window without the saved cursor raises a gap alert, but a shorter feed without overlap cannot prove whether events aged out after 30 days. GitHub may omit or delay events even when the cursor overlaps. The initial poll cannot establish what happened before it started. See [Event handling and recovery](Event-Handling-and-Recovery) for reconciliation and gap acknowledgment.

The MVP does not isolate concurrent changes made outside Inletd to the same working directory. Check the working tree and target issue or PR before retrying or submitting another command. An external agent may have produced effects before failing, so job state cannot guarantee exactly-once results.

## Agent shutdown and platforms

Agent execution is supported on Linux and macOS. Inletd starts each agent command in a separate process group. When `agent_timeout_seconds` expires, or Inletd receives Ctrl+C or SIGTERM, it sends SIGTERM to the group, allows one second for cleanup, then sends SIGKILL and waits up to one more second for the agent to exit. A stopped command leaves its job in `running` state for manual reconciliation because it may have made external changes. Other platforms reject agent execution because process tree cleanup is not implemented there. Abrupt termination of Inletd itself, such as SIGKILL or a machine crash, cannot run this cleanup.
