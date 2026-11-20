---
id: incident-postmortem-from-logs
title: Incident postmortem from logs
tags: [incident, operations]
variables: [service, log_source, window]
---
Write a blameless postmortem for the incident in {{service}} during {{window}}, using only the logs in {{log_source}}.

1. Establish the timeline: first error, first alert, detection, mitigation, and recovery, with timestamps in UTC.
2. Identify the trigger and the contributing factors. Quote the exact log lines that support each claim.
3. Measure impact: error count and rate, affected requests or tasks, duration, and any data loss, with the query used to get each number.
4. Separate facts from hypotheses. Mark anything the logs cannot prove as an open question.
5. List action items: each with an owner role, a concrete change, and how success will be measured. Redact secrets, tokens and personal data.

Acceptance:
- Timeline has no gaps larger than the logs' own resolution, and gaps are stated.
- Every claim cites a log line or query.
- No blame on individuals and no secrets or personal data in the output.

Report the postmortem in the sections: summary, timeline, impact, root cause, contributing factors, action items, open questions.
