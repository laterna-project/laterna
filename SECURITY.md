# Security

## Reporting a vulnerability

Please do not open a public issue. Use
[Report a vulnerability](https://github.com/laterna-project/laterna/security/advisories/new) on
GitHub: the report stays private until a fix is released.

Say which version you run, how the problem can be reproduced and what an attacker gains.

## Supported versions

Fixes go into the latest release. The `edge` Docker image follows the `develop` branch and gets
them first.

## What to expect from the server

A Laterna server is meant to be reachable from the Internet behind a reverse proxy. The limits it
applies (sign-in throttling, bounded password hashing, message sizes, trusted proxies) are
described in [docs/design/accounts.md](docs/design/accounts.md#limits-against-abuse). Media
addresses that work without a header (images, streams, subtitles) rely on unguessable paths, as
explained in [docs/design/api.md](docs/design/api.md#bytes-over-plain-http).
