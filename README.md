# beeper-matrix-proxy

**Expose rooms from a private Matrix homeserver inside Beeper Desktop, using a
stock Beeper client and a bridgev2 custom bridge.**

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Matrix](https://img.shields.io/badge/Matrix-bridge-black?logo=matrix&logoColor=white)](https://matrix.org/)
[![Beeper](https://img.shields.io/badge/Beeper-bridgev2-35C759)](https://developers.beeper.com/bridges/self-hosting)
[![Status](https://img.shields.io/badge/status-experimental-orange)](#current-status)

`beeper-matrix-proxy` is an experimental Matrix-to-Beeper proxy built on
[`mautrix-go` bridgev2](https://pkg.go.dev/maunium.net/go/mautrix/bridgev2).
It treats a normal Matrix homeserver as the remote network and exposes joined
rooms inside Beeper through Beeper's self-hosted bridge flow (`bbctl`).

It does **not** patch Beeper Desktop. The bridge tries to speak the data contract
that Beeper already understands: room features, Matrix events, media metadata,
portal rooms, and appservice websocket traffic.

## The Short Version

| Question | Answer |
|---|---|
| Can I sign into an arbitrary Matrix homeserver directly from Beeper Desktop? | Not generally. Beeper Desktop is designed around Beeper accounts and Beeper-managed bridge accounts. |
| Can this project show rooms from my private Matrix server in Beeper? | Yes, that is the goal: private Matrix homeserver -> this bridge -> Beeper. |
| Can this reuse Beeper Cloud's existing WhatsApp/Telegram/Signal bridges on my own Synapse? | Not directly. Those bridges are registered to Beeper's homeserver and account model. |
| Can I run official/community bridges for my own Synapse instead? | Yes. Run the upstream Matrix bridge against your Synapse as its own appservice. |
| Can one bridge feed both Beeper and my Synapse? | Usually not safely from the same database. Run separate bridge instances or build a dedicated fanout layer. |

## Architecture

```mermaid
flowchart LR
  A["Private Matrix homeserver<br/>Synapse, Dendrite, Conduit"] -->|/sync, media, state| B["beeper-matrix-proxy<br/>mautrix-go bridgev2"]
  B -->|appservice websocket| C["Beeper bridge manager<br/>bbctl / Hungryserv"]
  C --> D["Beeper Desktop<br/>stock client"]
  D --> C --> B --> A
```

Beeper's official self-hosting docs describe `bbctl` as a tool for running
self-hosted bridges with a Beeper account. Beeper's bridge metadata also
distinguishes cloud, self-hosted, local, and platform-sdk providers. This matters
because a Beeper Cloud bridge is not a portable appservice registration that can
simply be pointed at your own Synapse.

## Current Status

This is a working research/prototype bridge, not a polished product. It already
contains the core compatibility fixes that made private Matrix rooms usable in
Beeper during testing:

- burst-safe Matrix `/sync`
- room discovery and portal creation
- message, edit, reply, and relation ID rewriting
- Beeper-compatible poll fallback normalization
- media reupload between homeservers
- conservative media size capabilities to avoid proxy-side HTTP 413 failures

The remaining work is mostly around completeness: direct media proxying, richer
voice/GIF behavior, receipts, typing notifications, and safe cleanup tooling for
old broken backfill events.

## Feature Matrix

Legend:

- **Supported**: implemented and covered by tests or live smoke testing
- **Partial**: implemented enough to be useful, but still missing edge cases or full E2E coverage
- **Planned**: intentionally not wired yet
- **Not supported**: not safe to expose as a native Beeper feature

| Feature | Status | Direction | Verification | Notes |
|---|---:|---|---|---|
| Text messages | Supported | Matrix -> Beeper, Beeper -> Matrix | Live smoke test | Plain `m.room.message` events round-trip. |
| Burst delivery | Supported | Matrix -> Beeper | Regression test + live 8/8 burst test | Remote sync timeline limit is raised to avoid losing fast messages. |
| Room discovery | Supported | Matrix -> Beeper | Live smoke test | Joined remote Matrix rooms are synced as Beeper portal rooms. |
| Room name/topic | Supported | Matrix -> Beeper | Code path | Uses Matrix room state during chat sync. |
| Replies | Supported | Both | Regression test + live raw-event check | Beeper-local event IDs are rewritten to remote Matrix IDs. |
| Threads | Partial | Both | Regression test | Thread root IDs are rewritten; deeper UI behavior needs more testing. |
| Reactions | Partial | Both | Code path | Add/remove paths exist; broader Matrix client compatibility needs tests. |
| Edits | Supported | Both | Regression test + live smoke test | Legacy Matrix edit fallback prefixes are stripped for Beeper rendering. |
| Redactions / deletes | Partial | Both | Code path | Live delete paths exist; historical cleanup requires explicit redaction tooling. |
| Images | Supported | Both | Regression test | Media is downloaded and reuploaded across homeservers. |
| Files | Supported | Both | Regression test | Same media path as images; upload size is capability-capped. |
| Videos | Partial | Both | Code path | Works as media; large files depend on real proxy and homeserver limits. |
| GIFs | Partial | Both | Code path | Metadata handling exists; GIF-to-MP4 transcoding is not implemented yet. |
| Voice messages | Partial | Both | Payload support | Voice metadata is supported; waveform generation needs more work. |
| Polls | Partial | Matrix -> Beeper | Regression test + log-level E2E | Poll starts are normalized with MSC1767 text fallbacks; votes/end need E2E tests. |
| Backfill / history | Partial | Matrix -> Beeper | Code path | Backfill APIs exist; safe placeholder cleanup is intentionally separate. |
| Avatars | Partial | Matrix -> Beeper | Code path | Downloadable avatars work; stale media needs direct-media proxying. |
| Typing notifications | Planned | Both | Not implemented | Needs ephemeral event wiring. |
| Read receipts | Planned | Both | Not implemented | Needs receipt mapping and rate limiting. |
| Native audio/video calls | Not supported | Both | Intentionally hidden | Custom bridges should emit call notices/links instead of fake native call UI. |
| End-to-end encryption | Planned | Both | Not implemented as a product feature | Needs a separate device, key, and trust model design. |

## Can This Reuse Existing Beeper Bridges?

Short answer: **not directly**.

Beeper's own bridges are Matrix bridges, but the running bridge account is tied
to where it runs:

| Existing bridge type | Can this proxy reuse it for your own Synapse? | Why |
|---|---:|---|
| Beeper Cloud bridge | No | It is registered to Beeper's infrastructure and Beeper account model, not your Synapse appservice namespace. |
| Beeper self-hosted bridge via `bbctl` | Not directly | `bbctl` generates Beeper-side appservice config. The same process/database should not be blindly attached to another homeserver. |
| Beeper local/on-device bridge | No | It behaves like a local account provider for Beeper, not a generic Matrix appservice for Synapse. |
| Official mautrix bridge run against your Synapse | Yes | Register it as an appservice on your homeserver using the bridge's normal docs. |
| Dedicated fanout bridge | Possible | A custom layer could mirror events into both Beeper and Synapse, but must solve dedupe, edits, redactions, media, and identity mapping. |

The practical patterns are:

1. **Private Matrix in Beeper**: use this project.
2. **WhatsApp/Telegram/Signal in your own Matrix server**: run the relevant
   official/community Matrix bridge against your own homeserver.
3. **Same external account in both Beeper and your Matrix server**: run two
   separate bridge instances if the upstream network allows it, or design a
   dedicated fanout bridge. Sharing one live bridge database between two
   homeservers is a recipe for broken rooms and duplicate state.

## Setup

### Requirements

- Go 1.25+
- `libolm`
- Beeper Bridge Manager (`bbctl`)
- a Beeper account with self-hosted bridge support
- a Matrix account on the remote homeserver you want to expose in Beeper

On macOS:

```bash
brew install libolm
```

### Build

```bash
CGO_CFLAGS="-I/opt/homebrew/opt/libolm/include" \
CGO_LDFLAGS="-L/opt/homebrew/opt/libolm/lib -lolm" \
go build -o beeper-matrix-proxy
```

### Configure the Remote Matrix Homeserver

```bash
export LOCAL_MATRIX_HS="https://matrix.example.com"
```

Optional environment variables:

| Variable | Default | Purpose |
|---|---:|---|
| `LOCAL_MATRIX_HS` | `https://matrix.example.com` | Remote Matrix homeserver used for user login and sync. |
| `LOCAL_MATRIX_INSECURE_TLS` | enabled unless set to `0` | Allows self-signed or private TLS during development. |
| `LOCAL_MATRIX_INITIAL_BACKFILL_LIMIT` | `0` | Initial history import limit. |
| `LOCAL_MATRIX_MAX_UPLOAD_SIZE` | remote media config | Caps the size advertised to Beeper when a proxy has a smaller real limit. |
| `BEEPER_MATRIX_PROXY_DIR` | current directory | Directory used by `run-bridge.sh`. |
| `BEEPER_MATRIX_PROXY_BINARY` | `./beeper-matrix-proxy` | Binary used by `run-bridge.sh`. |
| `BEEPER_BBCTL` | `bbctl` | `bbctl` binary path. |

### Generate Config

```bash
go run . --generate-example-config -c config.yaml
go run . -g -c config.yaml -r registration.yaml
```

Fill the generated config with the appservice values from Beeper Bridge Manager.
Keep these files local:

- `config.yaml`
- `registration.yaml`
- bridge databases
- logs
- built binaries

They are ignored by `.gitignore`.

### Run

```bash
export BEEPER_MATRIX_PROXY_DIR="$PWD"
export BEEPER_MATRIX_PROXY_BINARY="$PWD/beeper-matrix-proxy"
./run-bridge.sh
```

Then start the login flow from Beeper and authenticate with the remote Matrix
homeserver username/password.

## Development

Run tests:

```bash
CGO_CFLAGS="-I/opt/homebrew/opt/libolm/include" \
CGO_LDFLAGS="-L/opt/homebrew/opt/libolm/lib -lolm" \
go test ./...
```

Important test coverage:

| Test area | File |
|---|---|
| Sync burst filter, edits, polls, relation rewriting | `connector/bridge_contract_test.go` |
| Media URLs and upload limits | `connector/media_test.go` |

## Design Notes

### Beeper Room Features

Beeper Desktop enables many compose actions from room state, especially
`com.beeper.room_features`. The bridge sets capabilities in code and bumps the
bridge info version when the feature contract changes, so existing rooms can
receive updated state.

### Event ID Mapping

Beeper and the remote Matrix homeserver have different event IDs for the same
logical message. Replies, thread roots, reactions, edits, and deletes must be
rewritten through the bridge database before crossing sides. Otherwise Beeper
IDs such as `$event:beeper.local` leak into the remote homeserver where they
cannot resolve.

### Media

Media is reuploaded instead of blindly forwarding `mxc://` URIs. That keeps
Beeper and the remote Matrix server from trying to dereference unknown media
repositories. The bridge can advertise a conservative upload size when the real
HTTP path has a smaller proxy limit than the homeserver media config claims.

### Calls

Native audio/video calls are intentionally not exposed as a supported capability.
The safe custom-bridge behavior is to convert incoming call events into
`m.notice` messages with an external join link. That fallback is planned.

## Roadmap

| Priority | Work item | Why it matters |
|---:|---|---|
| 1 | Direct media proxy support | Fix stale avatars and old media without Synapse 502 failures. |
| 1 | Safe ghost cleanup tool | Redact old placeholder events without hand-editing databases. |
| 2 | Call notices | Preserve call awareness without pretending Beeper has native custom-bridge calls. |
| 2 | Voice waveform fallback | Make voice notes render reliably when the source client omits waveform data. |
| 2 | Poll vote/end round-trip | Finish full MSC3381 behavior in both directions. |
| 3 | Typing notifications | Improve real-time feel. |
| 3 | Read receipts | Improve parity with normal Beeper rooms. |
| 3 | Optional GIF transcoding | Reduce large GIF upload failures and improve autoplay behavior. |

## Safety

This bridge creates real Matrix events. Test in small rooms first, keep backups
of bridge databases, and use dry-runs for cleanup/redaction tooling.

## References

- [Beeper self-hosted bridges](https://developers.beeper.com/bridges/self-hosting)
- [Beeper bridge metadata providers](https://developers.beeper.com/desktop-api-reference/cli/resources/bridges)
- [Beeper bridge-manager](https://github.com/beeper/bridge-manager)
- [mautrix-go bridgev2](https://pkg.go.dev/maunium.net/go/mautrix/bridgev2)

## License

No license has been selected yet. Until a license is added, treat this repository
as source-available rather than open-source.
