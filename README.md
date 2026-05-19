# beeper-matrix-proxy

`beeper-matrix-proxy` is an experimental Matrix-to-Beeper custom bridge built on
[`mautrix-go` bridgev2](https://pkg.go.dev/maunium.net/go/mautrix/bridgev2).

It treats a normal Matrix homeserver as the "remote network" and exposes its
rooms inside Beeper through Beeper's self-hosted bridge flow (`bbctl`). The goal
is to make private Matrix rooms usable from the stock Beeper Desktop client
without patching Beeper's app bundle.

## Why This Exists

Beeper Cloud does not federate with arbitrary Matrix homeservers. A private
Synapse, Dendrite, or Conduit server therefore cannot simply join Beeper by
federation. This project uses the supported Application Service bridge path
instead:

```text
Private Matrix homeserver <-> beeper-matrix-proxy <-> Beeper bridgev2 / Hungryserv <-> Beeper Desktop
```

The bridge focuses on the Beeper Desktop data contract:

- room capabilities via `com.beeper.room_features`
- stable remote/local event ID mapping
- Matrix media reupload in both directions
- history backfill with dedupe-aware IDs
- Beeper-friendly payloads for edits, replies, polls, GIFs, and voice messages

## Current Feature Matrix

| Feature | Status | Direction | Notes |
|---|---:|---|---|
| Text messages | Supported | Matrix -> Beeper, Beeper -> Matrix | Basic `m.room.message` text round-trips. |
| Fast message bursts | Supported | Matrix -> Beeper | Remote `/sync` timeline limit is raised to avoid dropping bursts. |
| Room discovery | Supported | Matrix -> Beeper | Joined remote Matrix rooms are synced as Beeper portal rooms. |
| Room names/topics | Supported | Matrix -> Beeper | Pulled from Matrix room state during chat sync. |
| Replies | Supported | Both | Beeper-local event IDs are remapped to remote Matrix event IDs before forwarding. |
| Threads | Partial | Both | Thread root IDs are remapped; deep client behavior still needs broader UI testing. |
| Reactions | Partial | Both | Add/remove paths exist with event ID remapping; more cross-client tests are needed. |
| Edits | Supported | Both | Matrix edit fallback prefixes such as `* ` are stripped before Beeper rendering. |
| Redactions / deletes | Partial | Both | Live message deletion paths exist; historical cleanup needs explicit redaction tooling. |
| Images | Supported | Both | Media is downloaded and reuploaded between homeservers. |
| Files | Supported | Both | Same media path as images; max upload size is advertised conservatively. |
| Videos | Partial | Both | Standard media works; very large files depend on proxy and homeserver limits. |
| GIF rendering | Partial | Both | GIF metadata flags are preserved/normalized where possible; transcoding is not implemented yet. |
| Voice messages | Partial | Both | Voice capability/payload support exists; waveform and codec coverage needs more device testing. |
| Polls | Partial | Matrix -> Beeper | Poll start payloads are normalized with MSC1767 text fallbacks; vote/end flows need more E2E tests. |
| Backfill / history | Partial | Matrix -> Beeper | Backfill APIs are implemented; old placeholder cleanup is deliberately not automatic. |
| Avatars | Partial | Matrix -> Beeper | Downloadable avatars work; stale remote media still depends on better direct-media handling. |
| Typing notifications | Not implemented | Both | Not wired yet. |
| Read receipts | Not implemented | Both | Not wired yet. |
| Native audio/video calls | Not supported | Both | Beeper custom bridges should expose calls as notices or links, not fake native call UI. |
| End-to-end encryption | Not a goal yet | Both | The bridge handles decrypted bridge traffic; production E2EE needs a separate design pass. |

## What Was Verified

The current implementation has regression tests for the most important bridge
contract fixes:

- Matrix sync burst preservation
- Beeper reply/thread relation remapping
- edit fallback cleanup
- poll payload text fallback normalization
- media URL handling and upload limit behavior

Run them with:

```bash
CGO_CFLAGS="-I/opt/homebrew/opt/libolm/include" \
CGO_LDFLAGS="-L/opt/homebrew/opt/libolm/lib -lolm" \
go test ./...
```

## Setup

### 1. Install requirements

- Go 1.25+
- `libolm` for Matrix crypto support
- Beeper bridge manager (`bbctl`)
- A Beeper account that can run self-hosted bridges
- A Matrix account on the remote homeserver

On macOS:

```bash
brew install libolm
```

### 2. Build

```bash
CGO_CFLAGS="-I/opt/homebrew/opt/libolm/include" \
CGO_LDFLAGS="-L/opt/homebrew/opt/libolm/lib -lolm" \
go build -o beeper-matrix-proxy
```

### 3. Configure the remote Matrix homeserver

Set the remote homeserver URL with `LOCAL_MATRIX_HS`:

```bash
export LOCAL_MATRIX_HS="https://matrix.example.com"
```

Optional environment variables:

| Variable | Default | Purpose |
|---|---:|---|
| `LOCAL_MATRIX_HS` | `https://matrix.example.com` | Remote Matrix homeserver used for user login and sync. |
| `LOCAL_MATRIX_INSECURE_TLS` | enabled unless set to `0` | Allows self-signed or private TLS during local development. |
| `LOCAL_MATRIX_INITIAL_BACKFILL_LIMIT` | `0` | Initial history import limit. |
| `LOCAL_MATRIX_MAX_UPLOAD_SIZE` | remote media config | Caps the size advertised to Beeper. Useful when a proxy returns HTTP 413 before Synapse does. |

### 4. Generate bridge config

```bash
go run . --generate-example-config -c config.yaml
go run . -g -c config.yaml -r registration.yaml
```

Fill in the Beeper bridge-manager config as usual for a bridgev2 custom bridge.
Do not commit `config.yaml`, `registration.yaml`, databases, logs, or binaries.
They are ignored by `.gitignore`.

### 5. Run with bbctl

```bash
export BEEPER_MATRIX_PROXY_DIR="$PWD"
export BEEPER_MATRIX_PROXY_BINARY="$PWD/beeper-matrix-proxy"
./run-bridge.sh
```

Then start the login flow from Beeper and authenticate with the remote Matrix
homeserver username/password.

## Design Notes

### Beeper room features

Beeper Desktop is mostly data-driven. It enables and disables compose actions
from Matrix room state, especially `com.beeper.room_features`. This bridge sets
capabilities in code and bumps the bridge info version when the feature contract
changes, so existing rooms can receive updated state.

### Event ID mapping

Beeper and the remote Matrix homeserver have different event IDs for the same
logical message. Replies, thread roots, reactions, edits, and deletes must be
rewritten through the bridge database before crossing sides. Otherwise Beeper
IDs such as `$event:beeper.local` leak into the remote homeserver where they
cannot resolve.

### Media

Media is reuploaded instead of blindly forwarding `mxc://` URIs. That keeps
Beeper and the remote Matrix server from trying to dereference unknown media
repositories. The current implementation intentionally advertises conservative
upload limits when `LOCAL_MATRIX_MAX_UPLOAD_SIZE` is set, because reverse
proxies often reject large uploads before Synapse can return a clean Matrix
error.

### Calls

Native audio/video calls are not currently exposed as a supported capability.
For custom Beeper bridges, the safe behavior is to convert incoming call events
into `m.notice` messages with a join link. That fallback is planned but not yet
implemented.

## Roadmap

- Direct media proxy support for stale avatars and older attachments
- Call notices with Element Call / Matrix room links
- Better GIF handling, including optional GIF-to-MP4 transcoding
- Voice waveform generation fallback for clients that do not provide one
- Full poll vote/end round-trip tests
- Safe dry-run and apply tooling for redacting old backfill placeholders
- Typing notifications and read receipts

## Safety

This project is young and bridge code can create real Matrix events. Test in a
small room first, keep backups of bridge databases, and use dry-runs for any
history cleanup or redaction tooling.

## License

No license has been selected yet. Until a license is added, treat this repository
as source-available rather than open-source.
