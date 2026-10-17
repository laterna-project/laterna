# Notifications

A request lands while its requester is asleep, an episode arrives while nobody is looking: without
notifications the only way to know is to open the app and look around. Each profile has a list of
what it was told about, and every device shows the same list.

## What is announced

| Kind | To whom | When |
|---|---|---|
| `request_pending` | the administrators | a request waits for approval |
| `request_approved` | the requester | someone else approved it |
| `request_declined` | the requester | someone else declined it; the text carries the reason |
| `request_available` | the requester | the catalog has what was asked for |
| `request_failed` | the requester and the administrators | the request could not be handed to its source |
| `request_choose` | the requester | the albums of an artist it asked for can be chosen |
| `new_episodes` | the profiles that follow the series | episodes arrived |

- Nobody is told about what they just did: an administrator's own request, approved at once, makes
  no notification, and neither does approving one's own request.
- "The administrators" are the profiles that can administer: those of enabled administrator
  accounts that carry no restriction. A kid profile on the administrator's account hears nothing.
- A profile **follows** a series when it is one of its favorites, or when it played or started one
  of its episodes. There is no separate "follow" switch to keep in sync with what people watch.
- Only an episode the catalog did not have is announced, to the followers who can see it (library
  and parental control, checked when the notification is written). Importing a library announces
  nothing: nobody follows its series yet. A file that is moved or replaced announces nothing either.
- Episodes that arrive together make one notification per series. The server waits until the
  series has been quiet for two minutes, and at most fifteen after the first episode. One episode is
  named and opens on itself; several are counted and open on the series.

## The list

`NotificationService` lists a profile's notifications, newest first, with the number it has not
read, and marks or deletes some or all of them.

- A notification is composed text (`notification.…` keys, see
  [Internationalization](i18n.md)), stored with its params, so that each device reads it in its
  own language.
- It names what to open: the item, when the profile still sees it, and the request with its
  poster.
- A profile keeps its last 200 notifications, for 60 days.
- A change (one arrived, some were read or deleted on another device) is announced to the devices
  of the profile through `NotificationsChanged`. Like every event it carries no state: the client
  reloads the list. That reaches the devices that are connected; the others can be told by web
  push.

Notifications are written after the fact and never fail what they report on: if the list cannot
be written, the request is still approved and the log says why.

## Web push

The list only helps someone who opens the app. A device can also be told while the app is closed,
through the push service of its browser ([RFC 8030](https://www.rfc-editor.org/rfc/rfc8030)).

- The browser subscribes with the server's public key (`GetPushConfig`) and hands its subscription
  to the server (`SubscribePush`): an address at its push service, a key and a secret.
- A subscription belongs to a device, that is to a session. It follows the profile picked on that
  device, and goes away with the session (sign-out, device revoked).
- When a notification is written for a profile that has subscribed devices, the job
  `notification.push` sends it to each of them: a small JSON object (kind, the server's name, the
  text in the language of the profile, the item and the request to open).
- Each message is encrypted for its device ([RFC 8291](https://www.rfc-editor.org/rfc/rfc8291)).
  The push service (Google, Mozilla, Apple, Microsoft, depending on the browser) carries it
  without being able to read it: it only learns that this server sent something to that device,
  and when.
- The server names itself to push services with a key pair created at the first start and kept
  ([VAPID](https://www.rfc-editor.org/rfc/rfc8292)): a browser subscribes for that key, so it must
  never change. The token names the public address of the server when it is https, else the
  project's address.
- Push is a courtesy on top of the list. A message that cannot be delivered is logged and not sent
  again; a subscription the push service no longer knows is forgotten.

Endpoints come from devices, so any signed-in user chooses where the server posts. The client
that calls push services therefore only connects to public addresses (checked on the address
actually dialed, so a name that resolves to the local network is refused too), follows no
redirect and uses no proxy. Endpoints are https only.

Everything is in `internal/webpush`, without a dependency: the encryption is checked against the
example of RFC 8291, and tests run against a fake push service that verifies the token and
decrypts as a device would.

A browser needs the page to be served over https to subscribe (or from localhost).

## Alerts: Discord, ntfy and a webhook

Notifications belong to a profile and live in the apps. **Alerts** tell the whole household, outside
them: a Discord channel, an ntfy topic (on ntfy.sh or a server of one's own), or any web service
that takes a JSON object (Home Assistant, a script). Administrators set them up
(`NotificationService.ListAlertChannels`, `SetAlertChannel`, `TestAlertChannel`), one channel of each
kind, each with the categories it wants:

| Category | Entries of the activity log |
|---|---|
| Requests | a request created, approved, declined, available, failed |
| New episodes | the episodes of a series that arrived, once the series has been quiet (one entry per series, followers or not) |
| Playback | a playback that starts: who, what, on which device |
| Problems | what is marked worth a look: a refused sign-in, a background job that failed |

- An alert **is an entry of the activity log**, written in the server's language. The entry and one
  `alert.send` job per channel that wants it are written in the same transaction, so nothing is
  lost on a restart, and writing an entry reads the channels from memory, not from the settings.
- **Discord** gets the text, under the server's name, with mentions turned off (a title holding
  "@everyone" pings nobody). **ntfy** gets the JSON form of its publishing API: title, message, a tag
  per category, a higher priority for problems, and the access token as `Bearer` if one is set.
  **The webhook** gets the server, the category, the kind of entry, the text, its key and params,
  the date and the item, signed with HMAC-SHA256 when a secret is set
  (`X-Laterna-Signature: sha256=…`).
- A Discord URL must be one of Discord. The others take any http or https address, the local
  network included: unlike a push endpoint, which any profile gives, a channel is chosen by an
  administrator.
- What does not arrive is tried again by the job queue (three attempts, increasing delay), except a
  refusal that will not change (4xx but 408 and 429). Each channel shows its last delivery and its
  last failure, in words. **The failure of an alert makes no alert**: a broken channel would feed
  itself.
- The URLs and tokens are secrets (a Discord webhook URL is enough to post in the channel). They
  are never sent back: a channel shows where it goes up to its secret part
  (`discord.com/api/webhooks/1234/…`).

## Not in this version

Choosing which kinds a profile wants.
