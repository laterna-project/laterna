# Watch parties

Watching together, each at home or each on their own screen in the same room: play, pause and
seeks are shared, and nobody counts "three, two, one". Three difficulties:

- **devices differ**: one plays the file as is, another gets a transcoded stream, a third burns
  in a subtitle. A stream common to all would suit nobody;
- **clocks and networks differ**: a "play" order arrives at different times and each device takes
  a different time to load;
- **a device that is loading** must neither be left behind nor block the others forever.

## Each member plays, the group shares a state

- Each member opens their own playback (`StartPlayback`, with their device profile). Progress
  and "played" are recorded as usual, on their profile.
- The group only shares its **state** (`internal/party`, pure logic): the queue, the current
  item, a status (`PAUSED`, `PLAYING`, `WAITING`) and a **reference position**: "at instant `at`
  (server time), the position was `position`".
- While playing, each device works out where it should be at any moment, with no further message.
  An increasing `version` lets it discard a state older than the one it has.

**Server time** comes from `GetServerTime` and from `server_time` on every stream message. The
device keeps the offset of the shortest of several round trips.

**Shared start**: a playback starts at `at` = now + **800 ms**. Everyone receives the state
before that, gets in position and starts at the same instant.

## Waiting

- A seek, a change of item or resuming after a pause puts the group in `WAITING`: each member
  loads at the requested position, then reports ready (`ReportPartyStatus`).
- The group starts again when all members that are **in sync** are ready, or after **15 s**: a
  device that is too slow will catch up.
- A member in sync that starts buffering during playback puts the group in waiting.
- A member who joins during playback is not "in sync" yet: they catch up without stopping anyone,
  then report ready, and are waited for like the others from then on.

## Correction on the device

The server only gives the reference; precision depends on the device. The development console
(`devtools/console`) is the reference implementation:

- drift under 40 ms: nothing;
- up to half a second: playback rate proportional to the drift, up to ±10%;
- beyond that (joining mid-playback, falling behind): a **catch-up seek**. The device seeks a
  little **ahead** of the group (1 s, doubled as long as loading takes longer), buffers, then
  starts at the instant the group gets there. Seeking to the current position would land late by
  the loading time;
- buffering the device causes itself does not put the group in waiting.

## Rules

- **Commands** (`ControlParty`): play, pause, seek, item of the queue, new queue. By default
  **any member commands**; the host can reserve commands (`host_only`). Only the host removes a
  member (whose device cannot come back with the code) and ends the party. When the host leaves,
  the oldest member becomes host.
- **Queue**: same expansion rules as playlists (a season gives its episodes, an album its
  tracks). The end of the item on one member's device moves the group to the next; other "ended"
  signals for the same item are ignored. 500 items at most.
- **Joining** is by **code** (6 unambiguous characters, case-insensitive), and only if the
  profile can see **everything** the group watches (libraries, parental control). A new queue
  must be visible to every member.
- A **member** is a device (session) on a profile: two devices of the same profile are two
  members.
- **Stream** (`WatchParty`): the state on opening, then each change, messages and reactions
  (500 characters of text, 32 for a reaction; nothing is stored), a heartbeat every 30 s, and the
  end with its reason. A subscriber that falls behind receives the current state.
- **In memory only**: a restart drops parties, and one is recreated in an instant. A member with
  no open stream for **30 s** leaves; the last to leave ends the party. At most 100 parties per
  server and 20 members per party.

## Results and limits

Measured in Chromium with two devices on a file whose audio is transcoded:

| Situation | Measure |
|---|---|
| Play requested while paused | group playing 0.58 s after the command, picture moving at 1.38 s |
| Seek of +30 s while playing | picture moving 0.91 s after the command |
| Drift between the two players | under 2 ms |
| Joining mid-playback, nothing loaded | in sync in 1.67 s without stopping the host |
| Device 2 s behind | caught up in 0.95 s without stopping the group |

There is no shared stream: each member costs their own playback, a transcode if one is needed,
as if they watched alone.

Not done: invitations other than the code, message history for a member who joins, voice or
video, shared audio and subtitle tracks (everyone picks their own).
