# Fritz!Box TV

A lightweight macOS desktop app for watching live TV from a Fritz!Box cable
modem (DVB-C). It loads the channel list from the box's m3u endpoint and
plays the RTSP streams with an embedded libmpv — video renders directly in
the app window.

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
make app    # build self-contained dist/FritzTV.app
make dmg    # build dist/FritzTV.dmg (drag-and-drop installer)
```

The dmg/app is ad-hoc signed: on first launch, right-click → Open to get
past Gatekeeper. macOS will also ask for Local Network permission — required
to reach the Fritz!Box.

## Usage

By default the channel list is fetched from iptv-org's public catalog
(`https://iptv-org.github.io/iptv/index.m3u`, ~13k free channels). For live
DVB-C TV from the Fritz!Box, point it at the box instead, e.g.
`-url http://192.168.178.1/dvb/m3u/tv.m3u`. Override with the `-url` flag or the
`FRITZTV_M3U` environment variable. The last successfully fetched list is
cached in `~/Library/Application Support/FritzTV/` so the app also starts
when the box is unreachable. On network changes (e.g. switching from
tethering back to the Fritz!Box Wi-Fi) the list is re-fetched automatically.

`-autoplay` starts the first channel immediately.

### Keys

With the channel list open:

| Key | Action |
| --- | --- |
| ↑ ↓ PgUp PgDn | navigate |
| type text | filter/search channels |
| Enter | play selected channel |
| Esc | clear search, then hide list |
| Tab | hide list |

While watching (list hidden):

| Key | Action |
| --- | --- |
| Tab / Enter / Esc | show channel list |
| ↑ ↓ | channel up/down |
| ← → | volume |
| m | mute |
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

## Debugging

- `FRITZTV_MPV_LOG=/path/mpv.log` writes mpv's verbose log to a file.
- `FRITZTV_DEBUG_SHOT=/path/shot.png` saves a window screenshot (video +
  overlay) a few seconds after startup.
