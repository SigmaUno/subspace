---
id: write-integration-test-http
title: Write an integration test over HTTP
tags: [tests, api]
variables: [service, endpoint]
---
Write an integration test for {{service}} that calls {{endpoint}} over real HTTP through the public API.

1. Start the server in-process on a temporary database and a random port. Do not mock the store or the handlers.
2. Set up state only through public endpoints or documented setup helpers, so the test uses the same paths as clients.
3. Cover the success path, the main validation error, and the authorisation failure for {{endpoint}}. Check status code, error code and response body.
4. Check the side effect through a second public read, for example a list or audit endpoint, not through internal fields.
5. Make the test deterministic: fixed clock or injected time, no sleeps, and unique IDs per test. Clean up with `t.Cleanup`.

Acceptance:
- Test runs under `go test -race` and passes repeatedly with `-count=20`.
- Only the public API and documented contract are asserted, matching the endpoint description in the API doc.
- No trivial tests added only to raise coverage.

Report the test names, the status codes checked and the `-count=20` result.
