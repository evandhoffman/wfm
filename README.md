# wfm

`wfm` is a terminal UI for getting a Linux machine onto Wi-Fi with as little ceremony as possible.

The project is meant for the awkward moment after a fresh install: you have a shell, you need network access, and you do not want to click through a desktop network applet or hand-edit backend config files. `wfm` ships as a single static Go binary, then shells out to whichever Wi-Fi stack the host already uses.

## Status

- Linux only
- Target architectures: `linux/amd64` and `linux/arm64`
- Must be run as root: `sudo wfm`
- Requires one supported host Wi-Fi stack to already be present:
  - NetworkManager via `nmcli`
  - iwd via `iwctl`
  - wpa_supplicant via `wpa_cli`
- Personal Wi-Fi networks are the target; corporate/EAP flows are not a product focus right now

## What It Does

- Detects the active Wi-Fi backend automatically
- Scans and lists visible SSIDs
- Marks saved and currently connected networks
- Connects immediately to open or already-known networks
- Prompts for a passphrase for new secured networks
- Shows a detail view with signal, band, channel, auth, BSSID, and link-speed data
- Writes structured runtime logs to `/tmp/wfm.log` without corrupting the TUI

## Supported Backends

`wfm` probes backends in this order and uses the first one that is actually available and responsive:

1. NetworkManager (`nmcli`)
2. iwd (`iwctl`)
3. wpa_supplicant (`wpa_cli`)

Current backend behavior:

- `nmcli`: scans, connects, detects saved SSIDs from NetworkManager connection profiles
- `iwctl`: scans, connects, detects saved SSIDs from `/var/lib/iwd`
- `wpa_cli`: scans, connects, detects saved SSIDs from `wpa_cli list_networks`, and attempts `save_config` after new connections

Longer term, D-Bus backends are the intended direction for NetworkManager and iwd. The current CLI-shell-out design is deliberate for v1 because it keeps the binary simple and works across common Linux setups.

## Install

The repository is configured to publish release assets named `wfm-linux-amd64` and `wfm-linux-arm64` on `v*` tag pushes. If a release for your platform is available, download that binary and place it somewhere on your `PATH`.

If you are building from source:

```sh
# linux/amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o wfm-linux-amd64 .

# linux/arm64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o wfm-linux-arm64 .
```

`CGO_ENABLED=0` matters here because the goal is a fully static binary with no glibc dependency.

## Usage

Run it with root privileges:

```sh
sudo wfm
```

Current key bindings:

- `↑` / `↓`: move through the network list
- `enter`: connect to the selected network
- `i`: open the detail view for the selected network
- `r` or `space`: rescan
- `esc`: cancel passphrase entry
- `q` or `ctrl+c`: quit

Current flow:

1. Detect backend
2. Scan for networks
3. Show network list
4. Connect immediately for open or saved networks
5. Prompt for a passphrase for new secured networks
6. Rescan after a successful connection so the list reflects the new state

The TUI keeps a small activity pane visible during scans, connects, and errors. More verbose diagnostics go to `/tmp/wfm.log`.

## Development

The primary development machine for this repo is macOS, so local development is mostly cross-compilation. The macOS build exists only so the codebase compiles cleanly during development; the program itself is Linux-only.

Project layout:

```text
main.go                     bubbletea model, state machine, entry point
wifi/backend.go             backend interface and shared types
wifi/detect_linux.go        backend auto-detection
wifi/nmcli_linux.go         NetworkManager backend
wifi/iwd_linux.go           iwd backend
wifi/wpasupplicant_linux.go wpa_supplicant backend
```

## Roadmap

Near-term work is being shaped by the current open issue tracker. The main items that still look relevant are:

- Better CLI help and friendlier non-root behavior (`--help`, `-h`, and clearer usage output) in #8
- Forget/remove saved networks from inside the TUI in #13
- Show the last scan timestamp so stale results are obvious in #14
- Improve log handling for end users, including bounding or rotating the logfile, in #15

There is also a longer-term architectural direction to replace shell-outs with D-Bus integrations where that materially improves reliability and observability.

## Limitations

- Root is required today
- No dedicated CLI flags yet
- Enterprise/EAP flows are not a first-class UX target
- The tool depends on the host system already having a supported Wi-Fi stack installed
