#!/bin/sh
# Checks the Compose deployment: every module, alone and in the usual combinations, makes a valid
# configuration, and the ones that need no outside account start and serve Laterna.
#
#   deploy/compose/test/test.sh [config|run|dns]...   (config and run by default)
#
# dns builds Caddy with the module of every DNS provider in config/caddy/conf/dns and reads the
# Caddyfile with each: it takes a few minutes, and catches a provider whose module stopped
# building.
#
# LATERNA_IMAGE and LATERNA_VERSION choose another image than the published one (a local build:
# LATERNA_IMAGE=laterna LATERNA_VERSION=dev). The tests run on a copy of deploy/compose, with
# test.env as .env and fake credentials.
set -eu

here=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
project=laterna-test
# Modules of the run in progress, whose logs a failure shows.
current=""
cleanup() {
  status=$?
  if [ "$status" -ne 0 ] && [ -n "$current" ]; then
    # shellcheck disable=SC2086
    (cd "$work" && compose $current -- logs --tail 50 >&2) || true
  fi
  if [ -f "$work/compose.yaml" ]; then
    (cd "$work" && docker compose -p "$project" down -v --remove-orphans >/dev/null 2>&1) || true
  fi
  docker network rm laterna-test-external >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

cp -R "$here/." "$work"
cd "$work"
cp test/test.env .env
if [ -n "${LATERNA_IMAGE:-}" ]; then echo "LATERNA_IMAGE=$LATERNA_IMAGE" >>.env; fi
if [ -n "${LATERNA_VERSION:-}" ]; then echo "LATERNA_VERSION=$LATERNA_VERSION" >>.env; fi
# The server runs as the user running the tests in modules/custom-user.yaml, so that it can write
# to the folders below.
printf 'PUID=%s\nPGID=%s\n' "$(id -u)" "$(id -g)" >>.env
cp dns.env.example dns.env
cp traefik-dns.env.example traefik-dns.env
mkdir -p test/backups test/config test/cache test/certs

# compose <modules...> -- <docker compose arguments>
compose() {
  files=compose.yaml
  while [ "$1" != "--" ]; do
    files="$files:modules/$1.yaml"
    shift
  done
  shift
  COMPOSE_FILE=$files docker compose -p "$project" "$@"
}

check_config() {
  echo "config: ${*:-base}"
  compose "$@" -- config -q
}

config() {
  check_config
  for m in modules/*.yaml; do
    check_config "$(basename "$m" .yaml)"
  done
  # The combinations README.md describes.
  check_config intel-gpu backups resources https-dns duckdns-updater
  check_config custom-user config-file https-internal
  check_config media-split https-public
  check_config media-nfs traefik
  check_config media-smb nginx no-ports
  check_config external-network no-ports traefik-labels
  check_config external-network no-ports caddy-labels
  check_config host-network https-internal
  check_config tailscale backups
  check_config cloudflared no-ports
}

# wait_for <url> [curl options...]: waits up to 60 s for the URL to answer.
wait_for() {
  target=$1
  shift
  i=0
  until curl -fsS -o /dev/null "$@" "$target" 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -gt 60 ]; then
      echo "$target did not answer within 60 s" >&2
      return 1
    fi
    sleep 1
  done
}

# https_ok: Laterna answers through the proxy at https://laterna.test:18443, over HTTP/2, and its
# web client comes back.
https_ok() {
  url=https://laterna.test:18443
  opts="--resolve laterna.test:18443:127.0.0.1 -k"
  # shellcheck disable=SC2086
  wait_for "$url/health" $opts
  # shellcheck disable=SC2086
  test "$(curl -fsS -o /dev/null -w '%{http_version}' $opts "$url/health")" = 2
  # shellcheck disable=SC2086
  curl -fsS $opts -H 'Accept: text/html' "$url/movies" | grep -q '<div id="app">'
  # shellcheck disable=SC2086
  curl -fsS $opts -X POST -H 'Content-Type: application/json' -d '{}' "$url/laterna.v1.ServerService/GetServerInfo" >/dev/null
}

# run <name> <modules...>: starts the modules, checks them with the function test_<name>, stops them.
run() {
  name=$1
  shift
  echo "run: $name"
  current="$*"
  compose "$@" -- up -d --build --quiet-pull --wait --wait-timeout 120 >up.log 2>&1 || {
    cat up.log >&2
    return 1
  }
  "test_$name"
  compose "$@" -- down -v --remove-orphans >/dev/null 2>&1
  current=""
}

test_base() {
  wait_for http://127.0.0.1:18096/health
}

test_caddy() {
  https_ok
  # The certificate comes from Caddy's own authority.
  compose https-internal -- exec -T caddy test -f /data/caddy/pki/authorities/local/root.crt
}

test_traefik() {
  https_ok
}

test_nginx() {
  https_ok
  # Laterna is only reachable through nginx.
  if curl -fsS -o /dev/null http://127.0.0.1:18096/health 2>/dev/null; then
    echo "Laterna publishes its port despite no-ports" >&2
    return 1
  fi
}

test_folders() {
  test_base
  for d in movies shows music books photos; do
    compose media-split custom-user backups config-file resources -- exec -T laterna test -d "/media/$d"
  done
  # The server writes its data to the host's folder.
  test -d test/config/data
}

test_labels() {
  # Laterna is on the outside network, under its name.
  docker run --rm --network laterna-test-external curlimages/curl -fsS -o /dev/null http://laterna:8096/health
}

runs() {
  run base
  run folders media-split custom-user backups config-file resources
  run caddy https-internal
  run traefik traefik
  openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=laterna.test \
    -addext subjectAltName=DNS:laterna.test \
    -keyout test/certs/privkey.pem -out test/certs/fullchain.pem 2>/dev/null
  chmod 644 test/certs/privkey.pem
  run nginx nginx no-ports
  docker network create laterna-test-external >/dev/null
  run labels external-network no-ports traefik-labels
}

dns() {
  echo "dns: every provider of config/caddy/conf/dns"
  docker run --rm \
    -v "$work/config/caddy/conf:/etc/caddy:ro" \
    -v "$work/test/site.caddy:/etc/caddy-sites/site.caddy:ro" \
    caddy:2-builder sh -euc '
      cd /etc/caddy
      modules=""
      for f in dns/*.caddy; do modules="$modules --with github.com/caddy-dns/$(basename "$f" .caddy)"; done
      # shellcheck disable=SC2086
      xcaddy build --output /tmp/caddy $modules >/tmp/build.log 2>&1 || { tail -n 30 /tmp/build.log; exit 1; }
      for f in dns/*.caddy; do
        p=$(basename "$f" .caddy)
        LATERNA_HOST=laterna.test DNS_PROVIDER=$p /tmp/caddy adapt --config Caddyfile.dns >/tmp/adapted.json 2>/tmp/adapt.log ||
          { echo "$p:"; cat /tmp/adapt.log; exit 1; }
        # Laterna and the site of another module get their certificate through the provider.
        subjects=$(grep -o "\"subjects\":\[[^]]*\]" /tmp/adapted.json)
        case $subjects in
        *\"laterna.test\"*) ;;
        *) echo "$p: no certificate for laterna.test: $subjects"; exit 1 ;;
        esac
        case $subjects in
        *\"laterna.test.example\"*) ;;
        *) echo "$p: no certificate for the other site: $subjects"; exit 1 ;;
        esac
        grep -q "\"name\":\"$p\"" /tmp/adapted.json || { echo "$p: the provider is not used"; exit 1; }
      done'
}

if [ $# -eq 0 ]; then set -- config run; fi
for step in "$@"; do
  case $step in
  config) config ;;
  run) runs ;;
  dns) dns ;;
  *)
    echo "unknown step $step (config, run, dns)" >&2
    exit 2
    ;;
  esac
done
echo "compose ok"
