---
id: api-contract-review
title: Review an API against its contract
tags: [api, review]
variables: [contract, implementation]
---
Check that {{implementation}} matches the contract in {{contract}}. Report mismatches; fix only if the task asks.

1. List every endpoint, method, path, parameter and tool in {{contract}}.
2. For each one, find the handler in {{implementation}} and compare: path and method, request fields and types, validation, defaults, status codes, error codes and response field names.
3. Check the rules the contract states that are not in a schema: ordering, pagination limits, timestamp format, ID formats, idempotency, and auth requirements per surface.
4. Classify each mismatch as breaking, additive, or documentation-only, and give the file and line on both sides.
5. Check that every error code the implementation can return is listed in the contract.

Acceptance:
- Every contract item has a status: matches, mismatch, or not implemented.
- Each mismatch cites the contract section and the code location.
- No fix is made unless the task explicitly asks for one.

Report the mismatch table sorted by severity and the count of items checked.
