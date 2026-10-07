#!/bin/sh
# Prepares Authelia (modules/authelia.yaml) before it starts. In /secrets: random keys and the key
# pair that signs ID tokens, created once, and the hash of OIDC_CLIENT_SECRET, made again at each
# start. In /config: the first user, from AUTHELIA_USER and AUTHELIA_PASSWORD, if there is no
# users.yml yet.
set -eu

cd /secrets
for name in session storage reset-password oidc-hmac; do
  [ -s "$name" ] || authelia crypto rand --length 64 --file "$name" >/dev/null
done
if [ ! -s oidc.pem ]; then
  authelia crypto pair rsa generate --bits 2048 --directory /secrets \
    --file.private-key oidc.pem --file.public-key oidc.pub.pem >/dev/null
fi
authelia crypto hash generate pbkdf2 --password "$OIDC_CLIENT_SECRET" --no-confirm |
  sed -n 's/^Digest: //p' >laterna-client.digest

if [ ! -s /config/users.yml ]; then
  digest=$(authelia crypto hash generate argon2 --password "$AUTHELIA_PASSWORD" --no-confirm | sed -n 's/^Digest: //p')
  cat >/config/users.yml <<USERS
# Authelia's users. A password hash comes from:
#   docker compose exec authelia authelia crypto hash generate argon2
users:
  $AUTHELIA_USER:
    displayname: "$AUTHELIA_USER"
    password: "$digest"
    email: "$AUTHELIA_EMAIL"
USERS
  echo "created the user $AUTHELIA_USER"
fi
