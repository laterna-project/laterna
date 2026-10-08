# Deploying Laterna with Docker Compose

A base file runs the server alone; modules add what a setup needs: media on a NAS, a GPU,
backups on another disk, HTTPS through one of several reverse proxies, access from outside, the
services Laterna works with (single sign-on, monitoring) and the upkeep of it all. Pick the
modules, list them in `.env`, and `docker compose` combines them.

Sonarr, Radarr, Prowlarr and the download clients behind a VPN are in
[laterna-stack](https://github.com/laterna-project/laterna-stack), which Laterna started from here
joins with `external-network` ([Sonarr, Radarr and downloads](#sonarr-radarr-and-downloads)).

- [Starting](#starting)
- [Coming from docker run](#coming-from-docker-run)
- [Combining modules](#combining-modules)
- [Modules](#modules)
- [Media and server](#media-and-server)
- [HTTPS and access](#https-and-access)
- [Services](#services)
- [Single sign-on](#single-sign-on)
- [Upkeep](#upkeep)
- [Platforms](#platforms)
- [Checking the files](#checking-the-files)

## Starting

Docker with Compose 2.24.4 or later (`docker compose version`), or Docker Desktop.

```sh
git clone --depth 1 --branch main https://github.com/laterna-project/laterna
cd laterna/deploy/compose
cp .env.example .env
```

Set `MEDIA_DIR` in `.env` to the folder holding the media, then:

```sh
docker compose up -d
```

Open http://localhost:8096 (or `http://<this machine's address>:8096` from another device) to set
the server up. `docker compose logs -f laterna` shows what it does.

- **Upgrading:** `docker compose pull && docker compose up -d`. `LATERNA_VERSION=0.6` follows the
  patches of 0.6.x; set the next minor version yourself after reading its release notes. The
  server backs its database up before migrating it.
- **These files:** `main` holds those of the latest release, and `git pull` brings the next
  release's. `--branch v0.6.0` keeps those of one release; `develop` has the ones being written.
- **Data:** the `config` volume holds the database, logs, images and backups: it is the one to
  keep. `cache` can be thrown away. `docker compose down` keeps both; `down -v` deletes them.

## Coming from docker run

A server started with `docker run` keeps its data here, in volumes or folders that these files do
not use by default: they would start an empty server. Stop and remove the old container
(`docker rm -f laterna`), then point these files at its data in `local.yaml` (listed last in
`COMPOSE_FILE`).

Volumes, as in [docs/install.md](../../docs/install.md#docker) (`-v laterna-config:/config`):

```yaml
volumes:
  config:
    name: laterna-config
    external: true
  cache:
    name: laterna-cache
    external: true
```

Folders of the host (`-v /srv/laterna/config:/config`):

```yaml
services:
  laterna:
    volumes:
      - /srv/laterna/config:/config
      - /srv/laterna/cache:/cache
```

`docker compose up -d`: the same server comes back, with its accounts, libraries and history.

## Combining modules

A module is a file in `modules/` that adds to the base or changes it. `COMPOSE_FILE` in `.env`
lists the base, then the modules, separated by `:` (`;` on Windows):

```sh
COMPOSE_FILE=compose.yaml:modules/intel-gpu.yaml:modules/https-dns.yaml:modules/duckdns-updater.yaml
```

Each module reads its own variables from `.env` (`.env.example` lists them by module) and stops
with a message naming any that is missing. `docker compose config` shows the result of the
combination without starting anything.

Do not edit the files here: an update would conflict with your changes. Put them in `local.yaml`
instead, last in `COMPOSE_FILE`, which git ignores. For instance, one more media folder:

```yaml
services:
  laterna:
    volumes:
      - /mnt/disk3/concerts:/media/concerts:ro
```

## Modules

✅: started by the CI on every change to these files, which checks that Laterna answers through it.
⚙️: the CI checks the configuration only, since running it takes an account, a domain or hardware
the CI does not have.

| Module | Use | |
|---|---|---|
| `compose.yaml` | The server alone, on port 8096 | ✅ |
| **Media** | | |
| `media-split` | One folder per kind of media, on several disks | ✅ |
| `media-nfs` | Media on a NAS over NFS, mounted by Docker | ⚙️ |
| `media-smb` | Media on a NAS or Windows share over SMB, mounted by Docker | ⚙️ |
| **Server** | | |
| `custom-user` | Another user than 1000, data in folders of the host | ✅ |
| `config-file` | Startup options in `config/laterna.toml` | ✅ |
| `backups` | Database backups on another disk | ✅ |
| `resources` | CPU and memory limits, log rotation | ✅ |
| `intel-gpu` | Transcoding on Intel graphics (VAAPI) | ⚙️ |
| `host-network` | The host's network, so TVs and apps find the server | ⚙️ |
| **HTTPS** | | |
| `https-dns` | Caddy, certificate through a DNS provider; nothing open to the Internet | ⚙️ |
| `duckdns-updater` | Keeps a free DuckDNS name on this machine's local address | ⚙️ |
| `https-internal` | Caddy with its own authority; no domain, no Internet | ✅ |
| `https-public` | Caddy for a server open to the Internet | ⚙️ |
| `traefik` | Traefik, certificate through any of about 150 DNS providers | ✅ |
| `nginx` | nginx with certificates you already have | ✅ |
| `npm` | Nginx Proxy Manager, set up in a web page | ⚙️ |
| `swag` | linuxserver.io's SWAG (nginx, certbot, fail2ban) | ⚙️ |
| **Access from outside** | | |
| `tailscale` | Your devices reach it from anywhere, with HTTPS, no port open | ⚙️ |
| `cloudflared` | A Cloudflare Tunnel (read the warnings) | ⚙️ |
| **A proxy that already runs** | | |
| `external-network` | Joins an existing Docker network (proxy, laterna-stack) | ✅ |
| `no-ports` | Publishes no port: only the proxy reaches the server | ✅ |
| `traefik-labels` | Labels for an existing Traefik | ✅ |
| `caddy-labels` | Labels for an existing caddy-docker-proxy | ⚙️ |
| **Services** | | |
| `prometheus` | Prometheus and Grafana, with a Laterna dashboard | ✅ |
| `jaeger` | Jaeger, for the server's traces | ✅ |
| `uptime-kuma` | Uptime Kuma, which warns when the server stops answering | ✅ |
| `jellyfin-import` | Jellyfin's data, to import its users and what they watched | ✅ |
| **Single sign-on** | | |
| `authelia` | Authelia: light, users in a file | ✅ |
| `authentik` | Authentik: flows, social logins, LDAP | ✅ |
| `keycloak` | Keycloak: LDAP and Active Directory, fine-grained policies | ✅ |
| `pocket-id` | Pocket ID: passkeys only | ✅ |
| `auth-caddy` | The provider over HTTPS through a Caddy module | ✅ |
| **Upkeep** | | |
| `diun` | Tells you when a newer image exists | ✅ |
| `watchtower` | Updates the server by itself (read the warning) | ✅ |
| `backup-offsite` | Nightly archive of the server's data to a disk, S3, WebDAV, SSH | ✅ |

## Media and server

**`media-split`.** `MOVIES_DIR`, `SHOWS_DIR`, `MUSIC_DIR`, `BOOKS_DIR` and `PHOTOS_DIR` appear
under `/media/movies`, `/media/shows`... in place of `MEDIA_DIR`; create a library for each.
One left unset shows up empty. For other folders, see `local.yaml` above.

**`media-nfs`, `media-smb`.** Docker mounts the share itself, read-only, when the server starts:
nothing goes in the host's `/etc/fstab`. `NFS_SERVER` and `NFS_PATH` (the exported folder), or
`SMB_SHARE` (`//nas/share`), `SMB_USER` and `SMB_PASSWORD` (without a comma). The NAS must let this
machine read the share. If the mount fails (`docker compose up` says why), use the NAS's IP
address rather than its name, and install the host's NFS or SMB tools (`nfs-common` or
`cifs-utils` on Debian and Ubuntu).

**`custom-user`.** For media only one user may read (a NAS account, Unraid's `nobody:users`,
99:100): the server runs as `PUID:PGID`, with its data in `CONFIG_DIR` and `CACHE_DIR`, which must
belong to that user:

```sh
mkdir -p /srv/laterna/config /srv/laterna/cache
sudo chown 1000:1000 /srv/laterna/config /srv/laterna/cache
```

**`config-file`.** Mounts `config/laterna.toml`, for the few options read at startup (log level,
forced encoder, CORS origins). Everything else is set from the administration screens.

**`backups`.** The server's database backups go to `BACKUP_DIR`, writable by user 1000 (or
`PUID`), rather than inside the `config` volume: a dead disk then loses no data.

**`resources`.** At most `LATERNA_CPUS` CPUs (4) and `LATERNA_MEMORY` of memory (2g), and logs
rotated at 3 × 10 MB. Transcoding on the CPU is what needs the most; going over the memory limit
stops the server.

**`intel-gpu`.** Gives the server `/dev/dri` and the host's `render` group, whose number goes in
`RENDER_GID` (`getent group render | cut -d: -f3`). The log line `usable H.264 encoders` then
starts with `h264_vaapi`. Intel Broadwell (2014) and later, Arc included; Linux only (Docker
Desktop gives containers no GPU). [docs/install.md](../../docs/install.md#intel-graphics-vaapi)
has the details.

**`host-network`.** The server shares the host's network, so that TVs and apps find it by
themselves (mDNS); Linux only. Port 8096 of the host is then the server's. With one of the Caddy
or nginx modules, set `LATERNA_UPSTREAM=host.docker.internal:8096`; the Traefik modules do not
work with it.

## HTTPS and access

Browsers install the app (PWA) and allow passkeys only from an HTTPS address. Which module:

- **On the local network, nothing to install on devices:** `https-dns`. A name of your domain (or
  a free DuckDNS name) points at this machine's local address, and the certificate comes from
  Let's Encrypt through the DNS provider: no port is opened on the box, and the server stays
  invisible from the Internet. Traefik does the same with more providers (`traefik`).
- **No domain at all:** `https-internal`, but every device must then trust Caddy's authority.
- **From outside, without opening anything:** `tailscale` (your devices only), or `cloudflared`.
- **Already open to the Internet:** `https-public`.
- **A proxy already running:** `external-network` and `no-ports`, plus `traefik-labels`,
  `caddy-labels`, or the proxy's own settings (below for nginx, Nginx Proxy Manager and SWAG).

Every module tells the server its proxy's addresses (`TRUSTED_PROXIES`, by default Docker's
default range 172.16.0.0/12), so that it logs the real addresses of devices. If Docker gave the
network another range, set it:

```sh
docker network inspect laterna_default -f '{{(index .IPAM.Config 0).Subnet}}'
```

Then set the server's public address (Administration, Authentication) to
`https://<LATERNA_HOST>`: passkeys, single sign-on and the sign-in links shown on TVs use it.

None of the proxies needs HTTP/3: Caddy is told not to announce it, since Docker publishes only
TCP 443 here.

### `https-dns`: Caddy and a DNS provider

1. A name for the server, pointing at this machine's address on the local network
   (`192.168.0.20`, say): an `A` record at your DNS provider, or `duckdns-updater` below.
2. `DNS_PROVIDER` and `LATERNA_HOST` in `.env`; the provider's credentials in `dns.env` (copy
   `dns.env.example`, keep your provider's lines). The token only needs to edit the domain's
   records.
3. `docker compose up -d --build`: Compose builds Caddy with the provider's module (a minute,
   once), and the certificate arrives within a minute or two (`docker compose logs caddy`).

Providers: `azure`, `bunny`, `cloudflare`, `desec`, `digitalocean`, `dnsimple`, `duckdns`,
`gandi`, `godaddy`, `hetzner`, `infomaniak`, `ionos`, `linode`, `namecheap`, `netcup`, `ovh`,
`porkbun`, `route53`, `scaleway`. The CI builds Caddy with each of them every week. Another
provider: the `traefik` module, or a file `config/caddy/conf/dns/<name>.caddy` for a module of
[caddy-dns](https://github.com/caddy-dns) of that name.

Some routers and DNS filters refuse a public name that answers with a private address (DNS
rebinding protection: OpenWrt, pfSense, OPNsense, some Fritz!Box, Pi-hole or AdGuard Home
options). If the name does not resolve on the local network, allow that domain there.

### `duckdns-updater`

With `DNS_PROVIDER=duckdns`: keeps `DUCKDNS_SUBDOMAIN.duckdns.org` on this machine's local
address, checked every 5 minutes, using the token of `dns.env`. `LATERNA_HOST` is then
`<DUCKDNS_SUBDOMAIN>.duckdns.org`. Under Docker Desktop, the container sees Docker's virtual
machine instead of the computer: set `LOCAL_IP` to the computer's address.

### `https-internal`: Caddy's own authority

`LATERNA_HOST` is a name the devices resolve to this machine (one given by the router or a local
DNS) or its IP address. Each device must trust Caddy's authority before the browser accepts the
site:

```sh
docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt .
```

- **Android:** Settings, Security, Encryption and credentials, Install a certificate, CA
  certificate (the path varies by maker).
- **iPhone, iPad:** open `root.crt` in Safari or send it by AirDrop, install the profile
  (Settings, General, VPN and device management), then turn it on in Settings, General, About,
  Certificate trust settings.
- **Windows:** open `root.crt`, Install certificate, Local machine, Trusted root certification
  authorities.
- **macOS:** open `root.crt` in Keychain Access (System), then Trust, Always trust.
- **Linux:** `sudo cp root.crt /usr/local/share/ca-certificates/laterna.crt && sudo
  update-ca-certificates` (Debian, Ubuntu). Firefox keeps its own list: Settings, Privacy and
  security, Certificates, Import.

The authority stays in the `caddy-data` volume: deleting it means installing the new one
everywhere.

### `https-public`: a server open to the Internet

The box forwards TCP ports 80 and 443 to this machine, `LATERNA_HOST` points at the box's public
address, and `ACME_EMAIL` receives Let's Encrypt's notices. Everyone can then reach the sign-in
page: keep the server up to date and use passkeys or long passwords. Prefer `https-dns` and
`tailscale` when only your own devices need it.

### `traefik`

The same as `https-dns`, without building anything, with any provider of
[lego](https://go-acme.github.io/lego/dns/): `TRAEFIK_DNS_PROVIDER` is the code on that page, and
`traefik-dns.env` (from `traefik-dns.env.example`) holds the variables it lists. To try without
reaching Let's Encrypt's limits, set `ACME_CA_SERVER` to its staging address (in `.env.example`),
then remove it and the `traefik-acme` volume. Traefik reads the labels through the Docker socket,
which gives control over Docker.

### `traefik-labels`, `caddy-labels`: a proxy that already runs

With `external-network` (`EXTERNAL_NETWORK`: the proxy's network) and usually `no-ports`. For
`traefik-labels`, `TRAEFIK_ENTRYPOINT` and `TRAEFIK_CERTRESOLVER` are the names in your Traefik
configuration (`websecure` and `letsencrypt` by default).

### `nginx`

`CERT_DIR` holds `fullchain.pem` and `privkey.pem`; restart nginx after renewing them.
[`config/nginx/laterna.conf.template`](config/nginx/laterna.conf.template) is also the model for an
nginx installed elsewhere: what matters is `proxy_buffering off`, long read and send timeouts, and
`client_max_body_size 32m` (theme files).

### `npm`: Nginx Proxy Manager

Open `http://<this machine>:81`, create the administrator, then a proxy host: the domain, scheme
`http`, host `laterna`, port `8096`; in SSL, request a certificate (over HTTP, or "Use a DNS
challenge" to open nothing); in Advanced:

```nginx
proxy_buffering off;
proxy_request_buffering off;
proxy_read_timeout 1h;
proxy_send_timeout 1h;
client_max_body_size 32m;
```

### `swag`

`SWAG_DOMAIN` is your domain, `LATERNA_HOST` must be `laterna.<SWAG_DOMAIN>`, and
`SWAG_DNS_PLUGIN` one of [SWAG's DNS plugins](https://docs.linuxserver.io/general/swag/). The
first start creates `/config/dns-conf/<plugin>.ini` in the `swag-config` volume: fill it in
(`docker compose exec swag vi /config/dns-conf/<plugin>.ini`), then `docker compose restart swag`.
[`config/swag/laterna.subdomain.conf`](config/swag/laterna.subdomain.conf) is mounted as SWAG's
proxy configuration.

### `tailscale`

1. In the [Tailscale admin console](https://login.tailscale.com/admin/dns), turn on MagicDNS and
   HTTPS certificates.
2. `TS_AUTHKEY`: an auth key (Settings, Keys, Generate auth key; it starts with `tskey-auth-`, an
   API access token is refused). Or leave it empty and open the link that
   `docker compose logs tailscale` prints within 10 minutes (`TS_BOOT_TIMEOUT`); after that the
   container restarts with a new link.
3. The server answers at `https://<TS_HOSTNAME>.<your tailnet>.ts.net` on every device of the
   tailnet, from anywhere, with a certificate browsers trust.

`TAILSCALE_SERVE=funnel` opens that address to the whole Internet (Tailscale Funnel), with the
same care as `https-public`. The tailnet's policy has to allow it first: in
[Access controls](https://login.tailscale.com/admin/acls), Funnel, Add Funnel to policy. The name
can then take up to 10 minutes to resolve. Tailscale limits Funnel's bandwidth without giving a
figure; devices with the Tailscale app connect directly instead.

### `cloudflared`: Cloudflare Tunnel

The tunnel reaches the server from the Internet without opening a port, but:

- Cloudflare's terms forbid serving video through its network outside its paid video products: an
  account streaming films can be limited or closed.
- Cloudflare decrypts all the traffic, passwords included.
- Uploads are capped at 100 MB, and an answer that takes more than 100 seconds to start is cut.

If that suits you: in the Cloudflare dashboard (Zero Trust, Networks, Tunnels), create a tunnel,
add a public hostname whose service is `http://laterna:8096`, and copy the tunnel's token to
`CLOUDFLARE_TUNNEL_TOKEN`. `tailscale` has none of these limits.

### Other setups

- **Laterna installed without Docker** (package, archive), proxy from here: set
  `LATERNA_UPSTREAM=host.docker.internal:8096` and start only the proxy
  (`docker compose up -d caddy`).
- **Your own VPN** (WireGuard, OpenVPN on the box): the devices connected to it reach the server
  as on the local network, and `https-dns` gives it HTTPS.

## Services

### Sonarr, Radarr and downloads

[laterna-stack](https://github.com/laterna-project/laterna-stack) brings the media: Sonarr,
Radarr, Prowlarr, qBittorrent behind a VPN, SABnzbd, Bazarr, Lidarr and others, all set up for
each other and for Laterna. Start it first, then Laterna from here on its network:

```sh
COMPOSE_FILE=compose.yaml:modules/external-network.yaml
EXTERNAL_NETWORK=laterna-stack
MEDIA_DIR=<the stack's DATA_DIR>/media
```

In Laterna, Administration, Sonarr and Radarr: `http://sonarr:8989` and `http://radarr:7878`
with their API keys, then let Laterna turn on Kodi metadata and install its webhook at
`http://laterna:8096`. The CI of each repository starts both together. Sonarr and Radarr that run
elsewhere work the same way: `external-network` on their network, or their address and the
server's.

### Monitoring

**`prometheus`.** Turn metrics on in Laterna (Administration, Observability) and copy the token it
shows once to `LATERNA_METRICS_TOKEN`, then `docker compose up -d`. Grafana,
http://<this machine>:3000 (user `admin`, `GRAFANA_PASSWORD`), opens on the Laterna dashboard:
playbacks and transcodes, requests and errors, library, jobs, memory, database size, time since
the last backup. Prometheus keeps 90 days (`PROMETHEUS_RETENTION`).

**`jaeger`.** The server sends the trace of each request, job and FFmpeg run to Jaeger,
http://<this machine>:16686: where the time of a slow page or a slow start goes. Traces stay in
memory until Jaeger restarts. Another collector that takes OTLP over HTTP/JSON works the same way,
with `OTEL_EXPORTER_OTLP_ENDPOINT` (and `OTEL_EXPORTER_OTLP_HEADERS` for a key) set on the server
in `local.yaml`.

**`uptime-kuma`.** http://<this machine>:3001: create the account, then an HTTP monitor on
`http://laterna:8096/health` and a notification (email, Telegram, ntfy, Discord...).

### Moving from Jellyfin

**`jellyfin-import`.** `JELLYFIN_DIR` is Jellyfin's data folder, the one holding
`data/jellyfin.db` (`/config` in Jellyfin's container: the host folder or volume behind it).
Then Administration, Jellyfin import, folder `/jellyfin`. Jellyfin can keep running: Laterna
works on a copy of its database. Jellyfin 10.11 or later.

## Single sign-on

Laterna signs in through an OpenID Connect provider, besides passwords and passkeys. One
provider module, plus:

1. `AUTH_HOST`: the provider's address, a name of the same domain as `LATERNA_HOST` that points
   at the same machine (`auth.example.com`). With `https-dns` and DuckDNS,
   `auth.<DUCKDNS_SUBDOMAIN>.duckdns.org` already points at the same address.
2. `OIDC_CLIENT_SECRET`: a long random value (`openssl rand -hex 32`), shared by the provider and
   Laterna.
3. HTTPS for the provider: `auth-caddy` with `https-dns` or `https-public`; the `traefik` module
   serves it by itself. Laterna must trust the provider's certificate, so `https-internal` does
   not work here.
4. In Laterna, Administration, Authentication: the public address, `https://<LATERNA_HOST>`,
   then the provider's address below, client `laterna`, the secret, the name of the button, and
   whether a provider user without a Laterna account gets one. A provider user signs in to the
   Laterna account of the same name.

| Module | Provider address | Users |
|---|---|---|
| `authelia` | `https://<AUTH_HOST>` | `users.yml` in the `authelia-config` volume; the first from `AUTHELIA_USER`, `AUTHELIA_PASSWORD`, `AUTHELIA_EMAIL` |
| `authentik` | `https://<AUTH_HOST>/application/o/laterna/` | in its pages, https://<AUTH_HOST>, as `akadmin` with `AUTHENTIK_PASSWORD` |
| `keycloak` | `https://<AUTH_HOST>/realms/laterna` | in the console, https://<AUTH_HOST>/admin, as `KEYCLOAK_ADMIN` with `KEYCLOAK_ADMIN_PASSWORD` |
| `pocket-id` | `https://<AUTH_HOST>` | in its pages, after https://<AUTH_HOST>/setup |

- **Authelia** makes its keys and the first user on its first start. A second factor (passkey,
  one-time code) for everyone: `AUTHELIA_POLICY=two_factor`. Its emails (password reset, new
  device) go to `/config/notifications.txt` in its volume; SMTP is set in `local.yaml`
  (`AUTHELIA_NOTIFIER_SMTP_*`).
- **Authentik** creates the provider and the application from
  [`config/authentik/laterna.yaml`](config/authentik/laterna.yaml). `AUTHENTIK_SECRET_KEY` and
  `AUTHENTIK_DB_PASSWORD`: long random values, kept forever.
- **Keycloak** imports the realm `laterna` and its client from
  [`config/keycloak/laterna-realm.json`](config/keycloak/laterna-realm.json) on its first start;
  later changes are made in its console. Users need a first name, last name and email, or
  Keycloak asks for them at their first sign-in.
- **Pocket ID** has no passwords: each user signs in with a passkey. Create the client in its
  pages: callback URL `https://<LATERNA_HOST>/auth/oidc/callback`, PKCE on; Laterna then takes the
  client ID and secret Pocket ID shows. `POCKET_ID_ENCRYPTION_KEY`: `openssl rand -base64 32`,
  kept forever. A user who lost their passkey gets a one-time sign-in link from
  `docker compose exec pocket-id /app/pocket-id one-time-access-token <user>`.

## Upkeep

**`diun`.** Checks every 6 hours whether a container of this machine has a newer image, and tells
you: the release notes come first, then `docker compose pull && docker compose up -d`. Where it
writes goes in `diun.env`, for instance ntfy:

```sh
DIUN_NOTIF_NTFY_ENDPOINT=https://ntfy.sh
DIUN_NOTIF_NTFY_TOPIC=my-secret-topic
```

The [notifiers](https://crazymax.dev/diun/notif/ntfy/): email, Telegram, Discord, Gotify,
Matrix, Pushover, Slack, webhooks...

**`watchtower`.** Pulls the image of `LATERNA_VERSION` every night at 4:00 and restarts the server
when it changed; no other container. It saves a step, but an update then happens without anyone
reading what it changes, and with `LATERNA_VERSION=latest` a new minor version comes in on its own:
prefer `diun`. Its notifications go in `watchtower.env`
(`WATCHTOWER_NOTIFICATION_URL`, [shoutrrr](https://nicholas-fedor.github.io/shoutrrr/) format).
This is the maintained fork; the original Watchtower is archived.

**`backup-offsite`.** Every night at 3:30, an archive of the server's `/config` (database, images,
logs, and its own database backups unless `backups` puts them elsewhere) to `BACKUP_DIR_OFFSITE`,
kept `BACKUP_RETENTION_DAYS` days (14). To send it to S3 or Backblaze B2, WebDAV (Nextcloud), SSH
or Dropbox instead, put the variables of
[docker-volume-backup](https://offen.github.io/docker-volume-backup/reference/) in
`backup-offsite.env`. To restore: stop the server, put the archive's `laterna-config` back in the
`config` volume, and if the live database inside is damaged, restore one of the backups next to
it from Administration, Backups.

## Platforms

- **Linux:** everything here. Docker Engine with the Compose plugin.
- **Windows, macOS (Docker Desktop):** everything but `host-network` and `intel-gpu`. Paths in
  `.env` are those of the computer (`C:/Users/Me/Videos` or `/Users/me/Movies`), and `COMPOSE_FILE`
  is separated by `;` on Windows.
- **NAS and web consoles** (Synology Container Manager, TrueNAS SCALE, OpenMediaVault, Unraid's
  Compose Manager, Portainer): those that run a Compose project from a folder take these files
  and `.env` as they are. For those that take a single pasted file, flatten the combination on a
  computer with the same `.env`: `docker compose config > laterna.yaml`; files mounted from
  `config/` must then be copied to the paths it shows. For media owned by another user than 1000,
  use `custom-user` (Synology: the user's `id`; Unraid: `PUID=99`, `PGID=100`).
- **Raspberry Pi and other arm64 boards:** the image exists for arm64 (64-bit OS). There is no
  hardware transcoding there: the server transcodes on the CPU, slowly, so prefer clients that
  play the files as they are.
- **Podman:** `podman compose` with Compose 2.24 or later as provider.

## Checking the files

[`test/test.sh`](test/test.sh) checks every module and the usual combinations, starts those marked
✅ and checks that Laterna answers through each, and builds Caddy with every DNS provider (`dns`
step). The [Compose workflow](../../.github/workflows/compose.yml) runs it on each change here and
every week.

```sh
sh test/test.sh          # config and run
sh test/test.sh dns      # every DNS provider, a few minutes
```
