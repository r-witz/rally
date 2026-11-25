# Security policy

## Supported versions

Only the latest release receives security fixes.

## Reporting a vulnerability

Report it privately through GitHub: Security tab, "Report a vulnerability"
(https://github.com/r-witz/rally/security/advisories/new). Do not open a
public issue.

You will get an answer within 7 days. Once a fix is released, the advisory is
published with credit to the reporter, unless they prefer otherwise.

## In scope

- A request that reaches a handler although it breaks the rules its types declare.
- A panic, or unbounded time or memory, caused by request input.
- A rejected value echoed back in an error response.
- An OpenAPI document that claims a rule the server does not enforce.

## Out of scope

Bugs in handler code, and server settings rally does not own (timeouts, TLS,
the MaxBodyBytes values you choose).
