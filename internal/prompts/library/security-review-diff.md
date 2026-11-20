---
id: security-review-diff
title: Security review of a diff
tags: [security, review]
variables: [base, head]
---
Review the change from {{base}} to {{head}} for security defects. Report only; do not edit code.

1. List every changed file and mark the ones that touch input parsing, authentication, authorisation, file or path handling, SQL or shell commands, crypto, logging, serialisation, or outbound network calls.
2. For each marked change, check: untrusted input reaching a sink without validation; authorisation checks missing on a new route or tool; secrets or tokens written to logs, errors or responses; path traversal; unbounded reads or loops; timing-unsafe comparisons; new default-open configuration.
3. For each finding, give file and line, the attacker-controlled input, the impact, and a minimal reproducer.
4. Rate each finding as high, medium or low, and say whether it is a regression introduced by this diff.
5. State what you checked and found clean, so the reader knows the coverage.

Acceptance:
- Every finding has file, line, input, impact and reproducer, or is marked as a suspicion needing confirmation.
- No findings are invented to fill the report.
- No secrets are copied into the report.

Report the findings table, then the list of files reviewed.
