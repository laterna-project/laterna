#!/bin/sh
# Points the DuckDNS name at the server's address on the local network: LOCAL_IP if given,
# otherwise the machine's, read again every 5 minutes. Under Docker Desktop, the container only
# sees Docker's virtual machine (192.168.65.x): LOCAL_IP is then required.
set -eu
: "${DUCKDNS_TOKEN:?set DUCKDNS_TOKEN in dns.env}"
last=""
while true; do
  ip=${LOCAL_IP:-$(ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if ($i == "src") print $(i + 1)}')}
  case "$ip" in
  192.168.65.*)
    echo "$(date -Iseconds) $ip is Docker Desktop's internal address: set LOCAL_IP in .env" >&2
    ;;
  "" | "$last") ;;
  *)
    if [ "$(wget -qO- "https://www.duckdns.org/update?domains=${DUCKDNS_SUBDOMAIN}&token=${DUCKDNS_TOKEN}&ip=${ip}")" = "OK" ]; then
      echo "$(date -Iseconds) ${DUCKDNS_SUBDOMAIN}.duckdns.org -> $ip"
      last=$ip
    else
      echo "$(date -Iseconds) DuckDNS refused the update (name or token?)" >&2
    fi
    ;;
  esac
  sleep 300
done
