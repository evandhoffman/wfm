# wfm — WiFi Manager TUI

TUI for setting up WiFi on Linux, distributed as a single static Go binary. Goal: someone can `curl` it down on a fresh Linux install and use it to get online, without needing extra application-side dependencies.

At runtime, `wfm` shells out to the host WiFi stack. It requires root and one of these environments to already exist on the target system:

- NetworkManager (`nmcli`)
- iwd (`iwctl`)
- wpa_supplicant (`wpa_cli`)

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
    wpasupplicant_linux.go      # wpa_supplicant backend (wpa_cli shell-outs)
  .github/workflows/
    release.yml                 # builds + publishes linux/amd64 and linux/arm64 on v* tag push
```

## Development environment

- **Primary dev machine is macOS** — binary cannot be run locally, only compiled
- Cross-compile for Linux amd64: `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o wfm-linux-amd64 .`
- Cross-compile for Linux arm64: `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o wfm-linux-arm64 .`
- `CGO_ENABLED=0` is required for fully static binaries (no glibc dependency)
- Test target: Raspberry Pi (arm64) and/or any Linux box
- Runtime requirement on Linux: run as root (`sudo wfm`)

## TUI stack

- [Bubbletea](https://github.com/charmbracelet/bubbletea) — Elm-architecture TUI framework
- [Bubbles](https://github.com/charmbracelet/bubbles) — prebuilt components (`table`, `textinput`, `spinner`)
- [Lipgloss](https://github.com/charmbracelet/lipgloss) — style/layout primitives

## TUI state machine

```
stateStartup → (Detect()) → stateScanning → (Scan()) → stateList
                                                            ↓ i
                                                       stateDetail
                                                            ↓ any key
                                                        stateList

stateList → enter (secured + unknown) → statePasswordEntry
                                            ↓ enter
                                      stateConnecting
                                            ↓ success
                                      stateScanning (rescan)
                                            ↓ error
                                        stateError → quit

stateList → enter (open or known) → stateConnecting
```

- Spinner shown during `stateStartup`, `stateScanning`, and `stateConnecting`
- `r` or space from the list triggers rescan
- `i` from the list opens a detail view for the selected network
- A fixed-height activity log pane is shown during list, rescan, connect, and error states
- Structured runtime logs go to `/tmp/wfm.log`; pre-TUI fatal messages still use stderr

## TUI behaviour

1. **Network list** — all visible SSIDs, status marker (`*` connected, `+` saved), signal bars (████ to ░░░░) + dBm, band, auth type, and `[connected]` / `[saved]` suffixes
2. **Password entry** — `bubbles/textinput` in EchoPassword mode; passphrase is never logged
3. **Known networks** — connect immediately with no password prompt (backend uses stored credentials)
4. **Detail screen** — per-network view shows signal label, BSSID, frequency/channel, channel width, auth, access-point count, and current link speed when connected
5. Corporate/EAP WiFi is out of scope in the product UX, though backends may report enterprise networks as visible/known

## WiFi backend

`Detect()` in `detect_linux.go` probes in order and returns the first working backend:

1. **NetworkManager** (`nmcli`) — Ubuntu, Fedora, Debian, Pop!_OS  
   Known networks: saved NM connections (`nmcli connection show`, type `802-11-wireless`)  
   Config written by NM automatically on connect

2. **iwd** (`iwctl`) — Arch Linux, some embedded/minimal systems  
   Known networks: `/var/lib/iwd/*.psk` files  
   Config written by iwd automatically on connect  
   Note: scan is async; backend sleeps 3 s after triggering scan

3. **wpa_supplicant** (`wpa_cli`) — implemented  
   Needed for: Raspberry Pi OS, Ubuntu minimal, Debian without NM  
   Known networks: `wpa_cli list_networks`  
   Config file: `/etc/wpa_supplicant/wpa_supplicant.conf` (must have `update_config=1`)  
   Control socket: auto-detected from `/run/wpa_supplicant/<iface>` or `/var/run/wpa_supplicant/<iface>`  
   Scan output: tab-separated BSSID/freq/signal(dBm)/flags/SSID — signal already in dBm  
   Connect flow: select known network when possible, otherwise `add_network` → `set_network ssid/psk` → `enable_network` → `select_network` → best-effort `save_config`

All backends also populate `ConnectionStatus` best-effort network details such as IPv4 address, gateway, DNS, and link speed.

D-Bus is the right long-term backend for both NM and iwd (avoids binary dependencies, real-time events) but shell-outs are acceptable for v1. The `Backend` interface is the seam that makes this swap trivial.

## Known test environment (Raspberry Pi)

- OS: Ubuntu (arm64)
- WiFi stack: **wpa_supplicant** + systemd-networkd (no NetworkManager, no iwd)
- Running service: `wpa_supplicant.service`
- Control socket and service wiring may vary by image; useful diagnostics are still:
  ```sh
  which wpa_cli
  ls /run/wpa_supplicant/
  systemctl cat wpa_supplicant | grep ExecStart
  ```

## Coding conventions

- No routine diagnostic `print()`/stdout logging — use `log/slog` for runtime diagnostics
- Platform-specific code lives in `_linux.go` / `_darwin.go` files; the repo currently also uses matching `//go:build` tags
- Prefer small, focused functions; bubbletea `Update` handlers delegate to helpers
- All backends must be unexported structs; callers always go through `Detect()`
- Passphrase must never appear in any log call
