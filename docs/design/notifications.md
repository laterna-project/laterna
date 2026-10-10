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
  reloads the list.

Notifications are written after the fact and never fail what they report on: if the list cannot
be written, the request is still approved and the log says why.

## Not in this version

Delivery to a device that is not connected (web push), a webhook for other programs, and choosing
which kinds a profile wants.
