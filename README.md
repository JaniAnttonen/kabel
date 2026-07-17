# kabel

A lightweight macOS desktop app for watching live TV from m3u playlists —
including the DVB-C streams a Fritz!Box cable modem serves on the local
network. Playback is an embedded libmpv: video renders directly in the app
window.

Written in Go: a single GLFW window, mpv render API for video, and the
channel list drawn by mpv itself as an OSD overlay. No web view, no
JavaScript, no transcoding.

## Prerequisites

```sh
brew install mpv dylibbundler
```

- `mpv` provides `libmpv` (used at build time and bundled into the .app —
  end machines don't need Homebrew).
- `dylibbundler` copies libmpv and its dependency tree into the app bundle.

## Build & run

```sh
make run    # run from source (dev)
make test   # unit tests
make app    # build self-contained dist/kabel.app
make dmg    # build dist/kabel.dmg (drag-and-drop installer)
```

The dmg/app is ad-hoc signed: on first launch, right-click → Open to get
past Gatekeeper. macOS will also ask for Local Network permission — required
to reach devices like a Fritz!Box.

## Usage

By default the channel list is fetched from iptv-org's public catalog
(`https://iptv-org.github.io/iptv/index.m3u`, ~13k free channels), grouped
by category with channels from your country floated to the top of each
group (country is inferred from the system time zone/locale — no location
permissions).

A Fritz!Box on the LAN is discovered automatically — via the `fritz.box`
DNS name, the default gateway, then an SSDP search for SAT>IP servers — and
its DVB-C channels are pinned on top of the list as a "Local" section. On
network changes both sources refresh, so carrying the app to another
Fritz!Box network just works.

To use a single specific playlist instead (disables discovery and merging),
pass `-url http://192.168.178.1/dvb/m3u/tv.m3u` or set the `KABEL_M3U`
environment variable. The last successfully fetched public list is cached in
`~/Library/Application Support/kabel/` so the app also starts when the
source is unreachable.

`-autoplay` starts the first channel immediately.

### Keys

With the channel list open:

| Key | Action |
| --- | --- |
| ↑ ↓ PgUp PgDn / scroll | navigate |
| type text | filter/search channels |
| Enter | play selected channel |
| Esc | clear search, then hide list |
| Tab | hide list |

While watching (list hidden):

| Key | Action |
| --- | --- |
| Tab / Enter / Esc | show channel list |
| ↑ ↓ / scroll | volume |
| ← → | channel up/down |
| m | mute |
| s | cycle subtitle tracks (auto-selected by region; manual choice remembered) |
| a | cycle audio tracks |
| Space | pause |
| f | fullscreen |
| q / Cmd+W | quit |

## Development

After cloning, activate the repo's git hooks once:

```sh
git config core.hooksPath .githooks
```

The pre-push hook runs `staticcheck` (pinned via the `tool` directive in
`go.mod`) and blocks the push on findings. CI (GitHub Actions) builds, runs
the tests, and runs staticcheck on every PR and push to main.

## Experimental

- `KABEL_PREBUFFER=1` keeps SAT>IP sessions warm for the adjacent channels
  and zaps from their buffers via a loopback proxy (instant channel
  switching). Needs a network path that sustains several concurrent UDP
  streams from the box; behind a NAT'd access-point hop the per-session
  rate can collapse, so it's off by default.

## Debugging

- `KABEL_MPV_LOG=/path/mpv.log` writes mpv's verbose log to a file.
- `KABEL_DEBUG_SHOT=/path/shot.png` saves a window screenshot (video +
  overlay) a few seconds after startup.
