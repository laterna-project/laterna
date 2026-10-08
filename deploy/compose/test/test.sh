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
# RUNS="caddy nginx" limits the run step to those runs (the names of the test_* functions).
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
  docker rm -f laterna-test-jellyfin >/dev/null 2>&1 || true
  docker network rm laterna-test-external >/dev/null 2>&1 || true
  # Some files there belong to the containers' users.
  docker run --rm -v "$work:/work" alpine rm -rf /work/test >/dev/null 2>&1 || true
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
mkdir -p test/backups test/config test/cache test/certs test/jellyfin test/ca test/offsite
# Single sign-on: test values only, never printed.
sso_password=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
cat >>.env <<EOF
AUTH_HOST=auth.test
OIDC_CLIENT_SECRET=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
AUTHELIA_USER=viewer
AUTHELIA_PASSWORD=$sso_password
AUTHELIA_EMAIL=viewer@laterna.test
KEYCLOAK_DB_PASSWORD=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
KEYCLOAK_ADMIN_PASSWORD=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
AUTHENTIK_SECRET_KEY=$(od -An -N40 -tx1 /dev/urandom | tr -d ' \n')
AUTHENTIK_DB_PASSWORD=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
AUTHENTIK_PASSWORD=$sso_password
POCKET_ID_ENCRYPTION_KEY=$(head -c 32 /dev/urandom | base64)
POCKET_ID_API_KEY=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
EOF

# compose <modules...> -- <docker compose arguments>
compose() {
  files=compose.yaml
  while [ "$1" != "--" ]; do
    case $1 in
    test/*) files="$files:$1.yaml" ;;
    *) files="$files:modules/$1.yaml" ;;
    esac
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
    case $m in
    # It adds to the Caddy of another module.
    modules/auth-caddy.yaml) check_config https-dns auth-caddy authelia ;;
    *) check_config "$(basename "$m" .yaml)" ;;
    esac
  done
  # The combinations README.md describes.
  check_config intel-gpu backups resources https-dns duckdns-updater
  check_config nvidia-gpu resources tailscale
  check_config custom-user config-file https-internal
  check_config media-split https-public
  check_config media-nfs traefik
  check_config media-smb nginx no-ports
  check_config external-network no-ports traefik-labels
  check_config external-network no-ports caddy-labels
  check_config host-network https-internal
  check_config tailscale backups
  check_config cloudflared no-ports
  check_config https-dns duckdns-updater auth-caddy authelia
  check_config https-public auth-caddy authentik
  check_config https-internal auth-caddy pocket-id
  check_config traefik keycloak
  check_config custom-user backup-offsite backups
  check_config prometheus jaeger uptime-kuma diun
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
  case " ${RUNS:-$name} " in
  *" $name "*) ;;
  *) return 0 ;;
  esac
  echo "run: $name"
  current="$*"
  compose "$@" -- up -d --build --quiet-pull --wait --wait-timeout 300 >up.log 2>&1 || {
    cat up.log >&2
    return 1
  }
  "test_$name"
  compose "$@" -- down -v --remove-orphans >/dev/null 2>&1
  current=""
}

# api <Service/Method> <JSON>: calls Laterna's API, as the administrator once setup_admin ran.
token=""
api() {
  curl -fsS -X POST -H 'Content-Type: application/json' ${token:+-H "Authorization: Bearer $token"} \
    -d "$2" "http://127.0.0.1:18096/laterna.v1.$1"
}

# setup_admin creates the server's administrator, with a password nobody knows, and keeps its
# session token.
setup_admin() {
  token=""
  password=$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')
  token=$(api AuthService/Setup "{\"username\":\"admin\",\"password\":\"$password\",\"device\":{\"name\":\"test.sh\",\"client\":\"test.sh\"}}" | jq -r .token)
  test -n "$token"
}

# retry <seconds> <command...>: runs the command every 2 seconds until it succeeds.
retry() {
  deadline=$(($(date +%s) + $1))
  shift
  until "$@"; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "gave up: $*" >&2
      return 1
    fi
    sleep 2
  done
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

prometheus_up() {
  curl -fsS http://127.0.0.1:19090/api/v1/targets | jq -e '.data.activeTargets[0].health == "up"' >/dev/null
}

jaeger_has_laterna() {
  curl -fsS http://127.0.0.1:26686/api/v3/services | jq -e '.services | index("laterna")' >/dev/null
}

test_monitoring() {
  setup_admin
  metrics_token=$(api SystemService/EnableMetrics '{}' | jq -r .token)
  echo "LATERNA_METRICS_TOKEN=$metrics_token" >>.env
  compose prometheus jaeger uptime-kuma -- up -d --wait prometheus >/dev/null 2>&1
  retry 90 prometheus_up
  # Every query of the dashboard is valid PromQL.
  jq -r '.panels[].targets[]?.expr' config/grafana/dashboards/laterna.json | while read -r expr; do
    curl -fsS -G --data-urlencode "query=$expr" http://127.0.0.1:19090/api/v1/query |
      jq -e '.status == "success"' >/dev/null || {
      echo "dashboard query fails: $expr" >&2
      return 1
    }
  done
  curl -fsS http://127.0.0.1:19090/api/v1/query --data-urlencode 'query=laterna_build_info' |
    jq -e '.data.result | length == 1' >/dev/null
  # Grafana: the dashboard is there, and its data source answers.
  retry 60 curl -fsS -o /dev/null -u admin:compose-test http://127.0.0.1:13000/api/dashboards/uid/laterna
  curl -fsS -u admin:compose-test http://127.0.0.1:13000/api/datasources/uid/prometheus/health |
    jq -e '.status == "OK"' >/dev/null
  # Jaeger receives the server's traces.
  retry 60 jaeger_has_laterna
  retry 60 curl -fsS -o /dev/null http://127.0.0.1:13001/
}

# jellyfin <method> <JSON or ""> <path>: calls the test Jellyfin's API, from inside its container.
jellyfin() {
  docker exec laterna-test-jellyfin curl -fsS -X "$1" -H 'Content-Type: application/json' ${2:+-d "$2"} \
    "http://127.0.0.1:8096$3"
}

test_jellyfin() {
  # A Jellyfin whose wizard created a user, in the folder the module mounts. It runs as the
  # server's user, 1000, as a Jellyfin next to Laterna would: its database is private to its user.
  chmod 777 test/jellyfin
  docker run -d --name laterna-test-jellyfin --user 1000:1000 -e JELLYFIN_CACHE_DIR=/config/cache \
    -v "$PWD/test/jellyfin:/config" jellyfin/jellyfin:10.11 >/dev/null
  retry 120 jellyfin GET '' /Startup/User >/dev/null
  jellyfin POST '{"UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}' /Startup/Configuration
  jellyfin POST '{"Name":"jellyfin-viewer","Password":"not-a-secret"}' /Startup/User
  jellyfin POST '' /Startup/Complete
  setup_admin
  api ImportService/PreviewJellyfinImport '{"path":"/jellyfin"}' |
    jq -e '[.users[].name] | index("jellyfin-viewer")' >/dev/null
}

# logs_have <service> <text>: the service's logs contain the text (any case).
logs_have() {
  # shellcheck disable=SC2086
  compose $current -- logs "$1" 2>&1 | grep -qi "$2"
}

test_operations() {
  # Diun and Watchtower start and plan their first run.
  retry 30 logs_have diun 'next run'
  retry 30 logs_have watchtower 'next scheduled run'
  # Watchtower only updates the server.
  test "$(docker inspect -f '{{index .Config.Labels "com.centurylinklabs.watchtower.enable"}}' "$project-laterna-1")" = true
  # A backup now: the archive holds the server's database.
  # shellcheck disable=SC2086
  compose $current -- exec -T backup-offsite backup >/dev/null 2>&1
  tar -tzf "$(ls test/offsite/laterna-*.tar.gz)" 2>/dev/null | grep -q 'laterna-config/data/'
}

# --- Single sign-on ---------------------------------------------------------------------------
# Laterna and a provider behind Caddy's own authority, under names that only exist inside Docker
# (laterna.test, auth.test, test/oidc.yaml): a curl container on the project's network plays the
# browser, with its cookies in test/jar.

browser() {
  docker run --rm --user "$(id -u):$(id -g)" --network "${project}_default" -v "$PWD/test:/t" \
    curlimages/curl -sS --cacert /t/ca/root.crt -b /t/jar -c /t/jar "$@"
}

# env_value <name>: a variable of .env.
env_value() {
  sed -n "s/^$1=//p" .env | tail -n 1
}

# trust_caddy: the browser and Laterna trust the authority of this run's Caddy.
trust_caddy() {
  # shellcheck disable=SC2086
  compose $current -- cp caddy:/data/caddy/pki/authorities/local/root.crt test/ca/root.crt >/dev/null 2>&1
  # Caddy keeps it private (0600): the server runs as another user than the tests.
  chmod 644 test/ca/root.crt
  # shellcheck disable=SC2086
  compose $current -- restart laterna >/dev/null 2>&1
  wait_for http://127.0.0.1:18096/health
}

# sso <issuer> <client ID> <client secret> <sign-in function> <account>: Laterna takes the
# provider, and a sign-in through it ends with a session for the account. The sign-in function
# gets the authorization URL and prints where the provider sends the browser back.
sso() {
  setup_admin
  api SystemService/UpdateSettings '{"publicUrl":"https://laterna.test"}' >/dev/null
  retry 180 browser -f -o /dev/null "${1%/}/.well-known/openid-configuration"
  api SystemService/SetOidcProvider \
    "{\"issuer\":\"$1\",\"clientId\":\"$2\",\"clientSecret\":\"$3\",\"name\":\"SSO\",\"autoCreate\":true}" >/dev/null
  start=$(token="" api AuthService/StartOidcLogin '{"device":{"name":"test.sh","client":"test.sh"}}')
  rm -f test/jar
  callback=$("$4" "$(echo "$start" | jq -r .authorizationUrl)")
  case $callback in
  "https://laterna.test/auth/oidc/callback?"*code=*) ;;
  *)
    echo "the provider did not send back a code: $callback" >&2
    return 1
    ;;
  esac
  confirm=$(browser -f "$callback" | sed -n 's/.*name="confirm" value="\([^"]*\)".*/\1/p')
  browser -f -o /dev/null --data-urlencode "confirm=$confirm" -d action=approve https://laterna.test/auth/oidc/callback
  token="" api AuthService/PollOidcLogin "{\"loginId\":\"$(echo "$start" | jq -r .loginId)\"}" |
    jq -e --arg account "$5" '.state == "OIDC_LOGIN_STATE_APPROVED" and .session.account.username == $account' >/dev/null
}

signin_authelia() {
  browser -f -o /dev/null -H 'Content-Type: application/json' \
    -d "{\"username\":\"viewer\",\"password\":\"$sso_password\",\"keepMeLoggedIn\":false}" \
    https://auth.test/api/firstfactor
  browser -o /dev/null -w '%{redirect_url}' "$1"
}

test_authelia() {
  trust_caddy
  sso https://auth.test laterna "$(env_value OIDC_CLIENT_SECRET)" signin_authelia viewer
}

signin_keycloak() {
  action=$(browser -f "$1" | sed -n 's/.*id="kc-form-login"[^>]*action="\([^"]*\)".*/\1/p' | sed 's/&amp;/\&/g')
  browser -o /dev/null -w '%{redirect_url}' --data-urlencode username=viewer \
    --data-urlencode "password=$sso_password" -d credentialId= "$action"
}

test_keycloak() {
  trust_caddy
  kcadm() {
    # shellcheck disable=SC2086
    compose $current -- exec -T keycloak /opt/keycloak/bin/kcadm.sh "$@"
  }
  kcadm config credentials --server http://127.0.0.1:9091 --realm master --user admin \
    --password "$(env_value KEYCLOAK_ADMIN_PASSWORD)" >/dev/null
  kcadm create users -r laterna -s username=viewer -s enabled=true -s email=viewer@laterna.test \
    -s emailVerified=true -s firstName=Viewer -s lastName=Test >/dev/null 2>&1
  kcadm set-password -r laterna --username viewer --new-password "$sso_password"
  sso https://auth.test/realms/laterna laterna "$(env_value OIDC_CLIENT_SECRET)" signin_keycloak viewer
}

# Authentik signs in through its flow executor, the API its pages use.
signin_authentik() {
  login=$(browser -o /dev/null -w '%{redirect_url}' "$1")
  case $login in
  *client_id=*) ;;
  *)
    echo "Authentik sent the sign-in to $login, without Laterna's request" >&2
    return 1
    ;;
  esac
  executor="https://auth.test/api/v3/flows/executor/default-authentication-flow/?query=$(jq -rn --arg q "${login#*\?}" '$q|@uri')"
  browser -f -o /dev/null "$executor"
  browser -f -L -o /dev/null -H 'Content-Type: application/json' \
    -d '{"component":"ak-stage-identification","uid_field":"akadmin"}' "$executor"
  next=$(browser -f -L -H 'Content-Type: application/json' \
    -d "{\"component\":\"ak-stage-password\",\"password\":\"$sso_password\"}" "$executor" | jq -r .to)
  browser -o /dev/null -w '%{redirect_url}' "https://auth.test$next"
}

test_authentik() {
  trust_caddy
  # Just after its first start, Authentik may still be creating its default flows; a sign-in
  # started before then loses where it was going.
  retry 180 browser -f -o /dev/null https://auth.test/api/v3/flows/executor/default-authentication-flow/
  sso https://auth.test/application/o/laterna/ laterna "$(env_value OIDC_CLIENT_SECRET)" signin_authentik akadmin
}

# Pocket ID has no password: a one-time access token signs in, as its command line gives one.
signin_pocket_id() {
  browser -f -o /dev/null -X POST "https://auth.test/api/one-time-access-token/$one_time_token"
  next=$(browser -o /dev/null -w '%{redirect_url}' "$1")
  case $next in
  *interaction=*)
    # The first time, the user consents to Laterna.
    next=$(browser -f -H 'Content-Type: application/json' -d '{"step":"consent"}' \
      "https://auth.test/api/oidc/interactions/${next##*interaction=}/complete" | jq -r .redirectUrl)
    next=$(browser -o /dev/null -w '%{redirect_url}' "https://auth.test$next")
    ;;
  esac
  echo "$next"
}

test_pocket_id() {
  trust_caddy
  pocket() {
    browser -f -H "X-API-KEY: $(env_value POCKET_ID_API_KEY)" -H 'Content-Type: application/json' "$@"
  }
  retry 60 pocket -o /dev/null https://auth.test/api/users
  user=$(pocket -d '{"username":"viewer","email":"viewer@laterna.test","firstName":"Viewer","lastName":"Test","isAdmin":false}' \
    https://auth.test/api/users | jq -r .id)
  client=$(pocket -d '{"name":"Laterna","callbackURLs":["https://laterna.test/auth/oidc/callback"],"isPublic":false,"pkceEnabled":true}' \
    https://auth.test/api/oidc/clients | jq -r .id)
  secret=$(pocket -d '{}' "https://auth.test/api/oidc/clients/$client/secrets" | jq -r .secret)
  one_time_token=$(pocket -d '{}' "https://auth.test/api/users/$user/one-time-access-token" | jq -r .token)
  sso https://auth.test "$client" "$secret" signin_pocket_id viewer
}

test_labels() {
  # Laterna is on the outside network, under its name.
  docker run --rm --network laterna-test-external curlimages/curl -fsS -o /dev/null http://laterna:8096/health
}

# Signing in to Tailscale takes an account, so the tunnel is not tried: only that the tailscale
# container reaches the server under the name Serve and Funnel forward to.
test_tailscale() {
  wait_for http://127.0.0.1:18096/health
  retry 30 compose tailscale -- exec -T tailscale wget -qO /dev/null http://laterna:8096/health
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
  run monitoring prometheus jaeger uptime-kuma
  run tailscale tailscale
  run jellyfin jellyfin-import
  # Without modules/custom-user.yaml, whose CONFIG_DIR test.env sets: the config volume.
  CONFIG_DIR="" run operations diun watchtower backup-offsite
  run authelia https-internal authelia auth-caddy test/oidc
  run keycloak https-internal keycloak auth-caddy test/oidc
  run authentik https-internal authentik auth-caddy test/oidc
  run pocket_id https-internal pocket-id auth-caddy test/oidc test/pocket-id
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
