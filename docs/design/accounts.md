# Accounts

A home server has to be simple to use from a TV, a phone and a browser, and safe when it is
reachable from the Internet. It serves a household: several people often share one account.

## Accounts, profiles and sessions

- An **account** is a person or a household. **Profiles** live under it, "Who's watching?" style.
  History, favorites, resume positions, language and theme belong to the profile. An account has
  at most 12 profiles and always keeps at least one unrestricted profile.
- A profile can have a **PIN** (4 to 8 digits), required to pick it, change it or delete it.
- **First start**: `AuthService.Setup` creates the first account, an administrator, with a
  profile of the same name. It is refused as soon as an account exists, checked inside the write
  transaction.
- Passwords and PINs are hashed with **argon2id** (19 MiB, 2 passes, 1 thread, PHC string). The
  hash is recomputed at sign-in if the parameters were strengthened. A dummy verification runs
  when the account does not exist, so response time reveals nothing.
- A **session token** is opaque: 256 random bits with a `lat_` prefix. Only its SHA-256 is
  stored. One session is one device, and it can be revoked. **Sliding expiry**: 90 days without
  use; the last use is written at most once every 10 minutes.
- There is no access token / refresh token pair. Every request reads the session from the
  database (tens of microseconds in SQLite), so revocation is immediate and clients stay simple.
- Changing the password signs out the other devices.
- Transport: `Authorization: Bearer <token>`.

How each method declares its access level is described in [API](api.md).

## Managed accounts

Administrators manage accounts through `AccountService`: create (with a profile of the same
name), rename, change role, disable, set a password, delete.

- A new password or disabling an account signs out all its devices.
- The server always keeps one enabled administrator; the check runs inside the transaction, after
  the change.
- An administrator cannot disable, demote or delete their own account.
- **Library access** per account: either all libraries, including future ones (the default), or
  a list. A deleted library leaves the lists. An administrator sees everything; promoting an
  account removes its restrictions, and restricting an administrator is refused.
- An administrator can deny offline downloads to an account.

## Parental controls

There are two levels: the account, set by an administrator, and the profile, set by the account
holder. Each carries a **maximum age** and a choice to **hide what has no rating**. The stricter
of the two applies, criterion by criterion (`ParentalControl.Combine`). A kid profile defaults to
age 10 with unrated content hidden.

**Rating to age** (`domain.RatingAge`), from what NFO files contain:

- United States: TV-Y 0, TV-Y7 7, TV-PG 10, TV-14 14, TV-MA 17; G 0, PG 10, PG-13 13, R 17,
  NC-17 18;
- France: "Tous publics" 0, "-10" 10, "-12" 12, "-16" 16, "-18" 18;
- forms such as "12+", "FSK 16", "US:PG-13", "FR-12";
- NR, "Not Rated" or nothing: unrated.

The age is computed when metadata is refreshed and stored with the item. An episode or a season
without a rating takes its series' rating at read time, so re-rating a series applies to all its
episodes at once.

**Filtering happens on the server, everywhere.** A `domain.Viewer` (profile, libraries, combined
control) is passed to every catalog read: lists, counts, search, genres, detail pages, the home
screen, played and favorite, progress, playback, collections, playlists, watch parties. Events
about a library the viewer may not see are not delivered. A forbidden item is **not found**, so
the answer does not reveal that it exists.

Music, books and photos carry no ratings, so age does not apply to them; otherwise a kid profile
that hides unrated content would see them all vanish. Library access still applies.

A **restricted profile** is a kid profile or one under parental control. It manages no profile,
no device and no password, and it **does not administer the server, even on an administrator
account**: `ACCESS_ADMIN` requires `Principal.CanAdminister`.

Things to know:

- On a shared account, protecting a child also depends on the PIN of the adult profile. Without
  one, a child can pick the adult profile. A separate account avoids that.
- Much content has no rating. "Hide unrated" can empty a child's catalog until the NFO files
  carry ratings.
- Images are served without authentication. Their address cannot be guessed and is only given
  with an item the caller may see.

## Signing in on a TV: device codes

Typing a user name and a password with a remote is painful, and a password typed on a shared
screen can be read over the shoulder. Laterna follows the flow of RFC 8628 (device authorization
grant) with four calls of its own contract; it is not an OAuth server.

1. The device calls `StartDeviceLogin` (no authentication) and receives a secret **device
   code**, a **code to display**, an expiry (10 minutes), an interval (5 s) and where to approve.
2. The person, on a signed-in device, enters the code. `GetDeviceLogin` shows which device is
   asking (name, app, platform, address); `ApproveDeviceLogin` or `DenyDeviceLogin` answers.
3. The device calls `PollDeviceLogin` at the interval: pending, approved (session and token, as
   with `Login`), denied or expired.

Details:

- The displayed code is 8 consonants ("BDWP-HQPK"); case, dash and spaces are ignored on input.
  No vowels means no words and no 0/O or 1/I confusion. With 20⁸ codes, 10 minutes of validity
  and 5 wrong codes per account per 15 minutes, it cannot be guessed.
- The device code is 32 random bytes, kept only as a hash and never displayed.
- Polling too fast is refused and lengthens the interval by 5 s (the "slow_down" of RFC 8628).
- The session is opened at approval, on the approver's account, for the requesting device, and
  handed over at the next poll, **once**. With `select_profile` the device lands on the
  approver's profile. A session approved but never collected is closed at expiry.
- A restricted profile approves nothing.
- Requests live in memory for 10 minutes; a restart drops them. At most 1,000 pending, 10 per
  address.

**Where to approve.** The server composes the address and returns it with the code:
`verification_url` to show next to the code, and `verification_url_complete`, code included, for
a QR code. The base is the `web_url` setting, the address of the web client; when it is empty,
`public_url` is used, for a web client served at the same address as the server. The path is
fixed by the server: `/device`, with the code in the `code` parameter (`app.DeviceLoginPath`). It
is part of the contract a web client keeps. A TV therefore needs to know nothing about the web
client. With no address known, both fields are empty and the device shows the code alone.

## Passkeys

Passkeys (WebAuthn) give a sign-in without a password that resists phishing and works the same on
the web, Android and iOS.

Verification is written in the tree (`internal/auth/webauthn`), without a dependency. The
reference Go libraries also verify every attestation format (TPM, SafetyNet, Apple…) and bring
about ten dependencies. Laterna does not need attestation: it exists to restrict which
authenticator models are accepted, which a home server does not do, and passkeys from Apple and
Google do not provide it anyway.

- A **CBOR reader reduced** to what WebAuthn uses, with bounded depth and sizes; tags, floats
  and indefinite lengths are refused.
- **COSE keys**: ES256, EdDSA and RS256 (2,048 bits at least), verified by the standard library.
- What protects the account is checked: the challenge (32 random bytes, five minutes, single
  use), the operation type, the **origin**, which must be allowed (this is where phishing fails),
  the RP ID hash, user presence and user verification, the signature, and a counter that must
  not go backwards.
- Attestation is requested as "none" and ignored.
- The parser is fuzzed.

For clients (`AuthService`):

- **Registration** on the signed-in account (`BeginPasskeyRegistration`,
  `FinishPasskeyRegistration`; 20 per account), listing and deletion.
- **Sign-in without a user name** (discoverable credential): `BeginPasskeyLogin`, then
  `FinishPasskeyLogin`, which opens a session like `Login`. Failures are limited per address and
  the refusal does not say why.
- Options and responses travel as JSON in the WebAuthn Level 3 format. Browsers read them with
  `PublicKeyCredential.parseCreationOptionsFromJSON` and answer with `toJSON()`; Android's
  Credential Manager and the iOS API use the same structures. Translating them into Protobuf
  messages would only add a layer to maintain.
- `GetServerInfoResponse.passkeys` says whether passkey sign-in is available. It requires the
  **public address** of the server (`public_url`): its host is the RP ID and its origin is
  allowed. A different RP ID (`passkey_rp_id`, a parent domain) and extra origins
  (`passkey_origins`: a web client hosted elsewhere, an Android app as
  `android:apk-key-hash:…`) can be set.

A passkey belongs to the account, not the profile; picking the profile follows, as after a
password. Verified with a software authenticator (`webauthntest`) and against Chromium's option
parsing; not yet tried with every platform authenticator.

## OpenID Connect

Many home servers rely on a shared identity provider (Authelia, Authentik, Keycloak). Laterna's
clients are apps (web, mobile, TV) that cannot all receive a redirect from the provider, so **the
server is the OIDC client and the device polls**.

1. The device asks for a sign-in (`AuthService.StartOidcLogin`) and receives the authorization
   address and a secret ID.
2. It opens the address in a browser: a tab, Custom Tabs, ASWebAuthenticationSession, or a phone
   through a QR code for a TV.
3. The person signs in at the provider, which sends them back to the **server**
   (`<public address>/auth/oidc/callback`).
4. The server exchanges the code, verifies the ID token and finds the account. It then shows a
   confirmation page: "Connect the device *Living room TV* (client, address) to the account
   *lea*?", with Allow and Deny.
5. On Allow, the account is linked (or created) and the session opened.
6. The device polls `PollOidcLogin` and receives its session once.

**Why a confirmation.** Without it, someone could start a sign-in from their own device and send
the authorization address to a victim ("look at this"). A provider sends an already signed-in
person straight back without asking anything, so one click would give the attacker a session on
the victim's account. The page names the device and its address, the answer comes back as a POST
with a single-use confirmation token, the page cannot be framed, has no script and sends no
`Referer`. A denial writes nothing.

**Checks** (`internal/oidc`, no dependency; the usual libraries would bring four modules for
what the standard library does in three hundred lines):

- discovery when the provider is saved: the announced issuer must be the configured one;
- authorization code flow with PKCE (S256), random state and nonce, each single use; the client
  secret goes in Basic authentication at the code exchange;
- the ID token must be signed with RS256 or ES256 by a key of the provider's JWKS ("none" and
  HS256 are refused; keys are reloaded for an unknown key ID at most once a minute), issued by
  the configured issuer for this client, not expired, with the right nonce and a subject.

**Accounts.** A provider identity (issuer, subject) is linked to an account and finds it again
even if the name changes later. At the first sign-in, the account with the same user name
(`preferred_username`, otherwise the email) is linked. If there is none, an account is created
when the setting allows it (ordinary account, all libraries, no usable password); otherwise the
sign-in is refused with the reason. A disabled account is refused.

The provider is trusted for user names. Only configure a provider where people cannot choose
their name freely: a private one is fine, a public one open to anyone is not, especially with
account creation on.

An administrator configures the provider with `SystemService.SetOidcProvider` (issuer, client ID,
secret, button label, account creation). The secret is never returned. One provider at a time.
Passwords and passkeys keep working on a linked account. Verified end to end against a fake
provider (`oidctest`), not yet against every real one.

## Passwords imported from Jellyfin

Accounts imported from Jellyfin keep their password: the PBKDF2 hash from Jellyfin stays valid
(`auth/legacy.go`, with bounded iterations) and is replaced by an argon2id hash at the first
successful sign-in. See [Importing from Jellyfin](jellyfin-import.md).

## Limits against abuse

A home media server nearly always ends up reachable from the Internet, often behind a reverse
proxy.

- **Failed attempts**: 5 failures per 15 minutes, per account and per address for passwords, per
  profile for PINs, per account for device codes.
- **Public entry points** (`Setup`, `Login`, `StartDeviceLogin`, `BeginPasskeyLogin`,
  `FinishPasskeyLogin`, `StartOidcLogin`) go through a token bucket per address: 10 requests at
  once, then one every 6 s. Beyond that, `RESOURCE_EXHAUSTED` with the time to wait. Polling
  calls have their own pace and are not counted.
- **argon2id is bounded**: two computations at a time, the rest wait. A hundred sign-ins sent at
  once, even with made-up names, would otherwise ask for nearly 2 GB. The peak is now 38 MiB,
  and an ordinary sign-in waits for nothing.
- **Message size**: 4 MiB per received message, once decompressed, which also guards against
  compression bombs.
- **Trusted proxies** (`server.trusted_proxies` / `LATERNA_TRUSTED_PROXIES`, addresses or CIDR
  ranges). When the connection comes from one of them, the client address is read from
  `X-Forwarded-For`: the first address, starting from the right, that is not itself a trusted
  proxy, since anything further left could have been invented by the client. Without trusted
  proxies the header is ignored. The same address is used for sessions, the activity log, the
  limits above and the access log.

A household never notices these limits; a bot does. Behind a reverse proxy, declare it, or the
whole household shares one address and therefore the same limits.
