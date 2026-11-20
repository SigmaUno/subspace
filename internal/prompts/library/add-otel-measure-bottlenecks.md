---
id: add-otel-measure-bottlenecks
title: Add OpenTelemetry and measure bottlenecks
tags: [observability, performance]
variables: [target]
---
Instrument {{target}} with OpenTelemetry and use the data to find bottlenecks.

1. Add traces around request boundaries, storage calls and outbound calls, and metrics for latency, throughput, queue depth and errors.
2. Keep attribute cardinality low and never record secrets, tokens or personal data.
3. Run a realistic load and read the traces: where is the time spent, and where is it spent waiting?
4. List the top bottlenecks ordered by share of total latency, with evidence.
5. Make instrumentation cheap enough to leave on and disabled by default when no exporter is configured.

Report the bottleneck list. Fix only what the task asks for.
