# wfm — WiFi Manager TUI

TUI for setting up WiFi on Linux, distributed as a single static Go binary. Goal: someone can `curl` it down on a fresh Linux install and use it to get online, with no dependencies.

## Target platforms

- `linux/amd64` — standard x86-64 laptops/desktops
- `linux/arm64` — Raspberry Pi 4/5, other ARM64 SBCs

## Distribution

- GitHub Releases: pre-built binaries for both targets, published on git tag push via GitHub Actions
- Naming convention: `wfm-linux-amd64`, `wfm-linux-arm64`

## Project layout

```
wfm/
  main.go                       # bubbletea model, state machine, entry point
  wifi/
    backend.go                  # Backend interface + Network/ConnectionStatus types
    detect_darwin.go            # stub: returns error (macOS dev builds only)
    detect_linux.go             # Detect() factory — probes nmcli, then iwctl, then wpa_cli
    nmcli_linux.go              # NetworkManager backend (nmcli shell-outs)
    iwd_linux.go                # iwd backend (iwctl shell-outs)
    wpasupplicant_linux.go      # wpa_supplicant backend (wpa_cli shell-outs) — TODO
  .github/workflows/
    release.yml                 # builds + publishes linux/amd64 and linux/arm64 on v* tag push
```

## Development environment

- **Primary dev machine is macOS** — binary cannot be run locally, only compiled
- Cross-compile for Linux amd64: `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o wfm-linux-amd64 .`
- Cross-compile for Linux arm64: `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o wfm-linux-arm64 .`
- `CGO_ENABLED=0` is required for fully static binaries (no glibc dependency)
- Test target: Raspberry Pi (arm64) and/or any Linux box

## TUI stack

- [Bubbletea](https://github.com/charmbracelet/bubbletea) — Elm-architecture TUI framework
- [Bubbles](https://github.com/charmbracelet/bubbles) — prebuilt components (list, textinput, spinner)
- [Lipgloss](https://github.com/charmbracelet/lipgloss) — style/layout primitives

## TUI state machine

```
stateStartup → (Detect()) → stateScanning → (Scan()) → stateList
                                                            ↓ enter (secured + unknown)
                                                    statePasswordEntry
                                                            ↓ enter
                                                    stateConnecting → stateScanning (rescan)
                                                            ↓ error
                                                    stateError → quit
```

- Spinner shown during stateStartup, stateScanning, stateConnecting
- `r` from list triggers rescan
- All diagnostic output goes to `/tmp/wfm.log` (never to stdout/stderr, which would corrupt the TUI)

## TUI behaviour

1. **Network list** — all visible SSIDs, signal bars (████ to ░░░░) + dBm, lock icon for secured, `[connected]` / `[saved]` tags
2. **Password entry** — `bubbles/textinput` in EchoPassword mode; passphrase is never logged
3. **Known networks** — connect immediately with no password prompt (backend uses stored credentials)
4. Corporate/EAP WiFi is out of scope

## WiFi backend

`Detect()` in `detect_linux.go` probes in order and returns the first working backend:

1. **NetworkManager** (`nmcli`) — Ubuntu, Fedora, Debian, Pop!_OS  
   Known networks: saved NM connections (`nmcli connection show`, type `802-11-wireless`)  
   Config written by NM automatically on connect

2. **iwd** (`iwctl`) — Arch Linux, some embedded/minimal systems  
   Known networks: `/var/lib/iwd/*.psk` files  
   Config written by iwd automatically on connect  
   Note: scan is async; backend sleeps 3 s after triggering scan

3. **wpa_supplicant** (`wpa_cli`) — **TODO / next to implement**  
   Needed for: Raspberry Pi OS, Ubuntu minimal, Debian without NM  
   Known networks: `wpa_cli list_networks`  
   Config file: `/etc/wpa_supplicant/wpa_supplicant.conf` (must have `update_config=1`)  
   Control socket: `/run/wpa_supplicant/<iface>`  
   Scan output: tab-separated BSSID/freq/signal(dBm)/flags/SSID — signal already in dBm  
   Connect flow: `add_network` → `set_network ssid/psk` → `enable_network` → `select_network` → `save_config`

D-Bus is the right long-term backend for both NM and iwd (avoids binary dependencies, real-time events) but shell-outs are acceptable for v1. The `Backend` interface is the seam that makes this swap trivial.

## Known test environment (Raspberry Pi)

- OS: Ubuntu (arm64)
- WiFi stack: **wpa_supplicant** + systemd-networkd (no NetworkManager, no iwd)
- Running service: `wpa_supplicant.service`
- wpa_supplicant config: unknown — need to check `systemctl cat wpa_supplicant` and `ls /run/wpa_supplicant/`
- Pending diagnostics before implementing backend:
  ```sh
  which wpa_cli
  ls /run/wpa_supplicant/
  systemctl cat wpa_supplicant | grep ExecStart
  ```

## Coding conventions

- No `print()` — use `log/slog` for all diagnostic output
- Platform-specific code in `_linux.go` / `_darwin.go` files (filename suffix is sufficient; no extra build tag needed)
- Prefer small, focused functions; bubbletea `Update` handlers delegate to helpers
- All backends must be unexported structs; callers always go through `Detect()`
- Passphrase must never appear in any log call
