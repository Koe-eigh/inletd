# JSON configuration

The local configuration file uses JSON. See [the minimal example](../examples/config.json).
At startup, the application calls `appconfig.Load(path)` to read typed sources,
routes, and local commands. Source names, command names, and event names are the
keys in their respective objects. Each route lists command names to run for that event.
Executable paths and arguments live only in the local file; event payloads do
not define commands.

The application-facing loader combines the shared `config.Config` settings with
`ntfy.SourceConfig`. The shared configuration package does not parse files or
depend on a transport.

Each ntfy source has `server_url`, `topic`, and an optional `token_env` name for
a credential environment variable. A command has `executable`, optional `argv`,
`working_directory`, `environment` (string keys and values), and
`timeout_seconds`. Optional settings are:

| JSON field | Default | Meaning |
| --- | ---: | --- |
| `queue.capacity` | 100 | Maximum pending events |
| `concurrency.max` | 1 | Maximum simultaneous commands |
| `commands.<name>.timeout_seconds` | 300 | Per-command timeout in seconds |

Call `EffectiveQueueCapacity`, `EffectiveMaxConcurrent`, or
`Command.EffectiveTimeout` to inspect effective values. Omitted numeric settings
remain nil in the parsed structure; explicit zero and negative values remain
available for subsequent validation. The loader rejects malformed JSON, extra
JSON values, and unknown fields. Validation of names, limits, references, and
credentials is handled by follow-up configuration work. Duplicate JSON field
names, including source, route, and command names, are rejected during loading.
