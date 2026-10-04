# Local discovery

On a TV, finding the server means typing "192.168.1.20:8096" letter by letter on an on-screen
keyboard. Laterna announces itself on the local network so clients find it without typing
anything.

Some servers answer a UDP broadcast on a port of their own. An iPhone or an Apple TV can only
send a broadcast with a special entitlement from Apple, whereas every platform can browse for a
DNS-SD service: NsdManager on Android, NWBrowser on Apple systems, avahi on Linux. So Laterna uses
DNS-SD over mDNS (RFC 6762 and 6763).

## What is announced

- Service **`_laterna._tcp`**, over IPv4. The server answers mDNS queries that look for it: PTR
  from the type to the instance, SRV (host and HTTP port), TXT, A, and service enumeration
  (`_services._dns-sd._udp`). A browse gets everything in one answer, with SRV, TXT and A
  attached to the PTR.
- **Instance and host are named after the server's ID**: `laterna-<uuid without dashes>`. Unique
  on the network without negotiation, and stable when the administrator renames the server. The
  display name is in the TXT record with the ID and the version: `id=…`, `name=…`, `version=…`,
  re-read at every answer.
- **The address given is on the client's network**: the address of the interface the query
  arrived on (on Windows, where the system does not say, the one whose network contains the
  client's address). Never the address of a Docker bridge or a VPN to a TV in the living room.

## Behavior

- The mDNS port is **shared** with avahi or mDNSResponder if they are running. The group is
  joined on every active interface, and interfaces are re-read every minute (Wi-Fi reconnected,
  new DHCP lease).
- The server announces itself at startup (twice, one second apart) and says goodbye at shutdown
  (records with a zero lifetime).
- The same record is not sent to the group again within one second, per interface (RFC 6762,
  section 6): a burst of identical queries gets one answer, without delaying a different query
  that arrived in between.
- An ordinary DNS query (source port other than 5353) gets a direct answer, as the RFC requires.
- **Only the local network is served** (RFC 6762, section 5.5): the machine itself and the
  networks an active interface is attached to. Any other source is ignored, with no answer of
  any kind. On a machine reachable from the Internet, port 5353 would otherwise tell anyone the
  server's ID, name and version, and serve as an amplifier towards a spoofed address. An unknown
  address makes the server re-read its interfaces, at most once every five seconds.

## Configuration

`server.discovery` / `LATERNA_DISCOVERY`, on by default. Discovery turns itself off if the server
only listens on loopback or on an IPv6 address, and announces a single address if the server
listens on one. A busy mDNS port never prevents the server from starting: it logs a warning.

**Docker**: multicast does not cross the default bridge network. Discovery needs `--network host`
(or an mDNS repeater); otherwise the address is typed in. The startup log lists the announced
addresses and, in a container, recalls this condition.

IPv6 is not announced.

## Dependency

`golang.org/x/net`, from the Go team, for two of its packages only: `ipv4` (join the group on
each interface, know the arrival interface, choose the outgoing one, which the standard library
cannot do without per-system code) and `dns/dnsmessage` (reading and writing DNS messages: a
parser exposed to the whole local network without authentication had better be well tested; it
already serves Go's own resolver).

`internal/discovery` knows neither the database nor the catalog. The zone and the answers are
pure functions with table tests, the query parser is fuzzed, and the real network path was tried
on Linux with avahi running on the same port.

## The public address

`GetServerInfoResponse.public_url` gives the public address set by the administrator, empty if it
is not set. It is the address of the API. To approve a device code, the server composes the
address of the web client page itself; see [Accounts](accounts.md).
