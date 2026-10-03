# Inletd

A demon for routing remote events into local workloads.

## The Idea

Recent development workflows increasingly involve local agents and tools that need to react to events happpening in remote systems. However, a local machine sits behind NAT and a firewall and has no public endpoint. Instead of exposing the local machine to the internet, `inletd` maintains an authenticated outbound subscription to an event transport such as [ntfy](https://ntfy.sh/).

## Architecture Overview

`inletd` is centered around a routing engine that connects remote events to trusted local actions.

```text

Event Source -> Routing Engine -> Local Actions

```

The system has three user-facing abstractions.

- **Event Sources** provide events.
- **Routing Engine** determine whice action should handle each event.
- **Local Actions** represent trusted workloads that can run on the local machine.

The routing engine is the core of `inletd`. Event sources and local actions are adapters around it, keeping ther router independent of any particular transport or workloads.

### Routing Strategies

- **Declarative Routing** lets the user directly define associations between events and actions.
- **Functional Routing** letes the user define a function that determines the destination at runtime. The function may be deterministic code, a classifier, or an LLM-based router such as Jev.

Both strategies expose the same fouting abstraction: `Event -> Action`

The difference is only how that decision is produced.

The overall mental model remains:

**Events enter, routing decides, actions run.**

