# Inletd

A demon for routing remote events into local workloads.

## The idea

Recent development workflows increasingly involve local agents and tools that need to react to events happpening in remote systems. However, a local machine sits behind NAT and a firewall and has no public endpoint. Instead of exposing the local machine to the internet, `inletd` maintains an authenticated outbound subscription to an event transport such as (ntfy)[https://ntfy.sh/]
