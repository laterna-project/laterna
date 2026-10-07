# Caddy with the module of one DNS provider (https://github.com/caddy-dns), for certificates
# obtained through DNS-01: the server never needs to be reachable from the Internet.
ARG CADDY_VERSION=2
FROM caddy:${CADDY_VERSION}-builder AS builder
ARG DNS_PROVIDER
RUN test -n "$DNS_PROVIDER" && xcaddy build --with "github.com/caddy-dns/${DNS_PROVIDER}"

FROM caddy:${CADDY_VERSION}
COPY --from=builder /usr/bin/caddy /usr/bin/caddy
