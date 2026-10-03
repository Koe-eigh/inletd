# Inletd

A demon for routing remote events into local workloads.

## The Idea

Recent development workflows increasingly involve local agents and tools that need to react to events happpening in remote systems. However, a local machine sits behind NAT and a firewall and has no public endpoint. Instead of exposing the local machine to the internet, `inletd` maintains an authenticated outbound subscription to an event transport such as [ntfy](https://ntfy.sh/).

## Architecture Overview

`inletd` is designed around a simple pipeline.

```text

Event Source -> Routing Rules -> Local Actions

```

These three concepts form the mental model of the system.

- **Event Source** defines where events enter the system.
- **Routing Rules** determine how events are mapped to actions.
- **Local Actions** represent trusted workloads that can run on the local machine.

The core design policy is separation of concerns. Event acquisition, routing and execution are independent layers, so each can evolve without affecting the others.

