# wfm — WiFi Manager TUI

Cross-platform TUI for setting up WiFi on Linux, written in Go.

## Project layout

- `main.go` — entry point and bubbletea model; will grow into sub-packages as the backend expands
- `go.mod` / `go.sum` — managed with standard `go get`/`go mod tidy`

## Development environment

- **Primary dev machine is macOS** — the binary cannot be run or tested locally
- Cross-compile for Linux: `GOOS=linux GOARCH=amd64 go build -o wfm-linux ./...`
- Test on a Linux host by copying the binary over (scp, etc.)

## TUI stack

- [Bubbletea](https://github.com/charmbracelet/bubbletea) — Elm-architecture TUI framework
- [Bubbles](https://github.com/charmbracelet/bubbles) — prebuilt components (list, textinput, spinner, …)
- [Lipgloss](https://github.com/charmbracelet/lipgloss) — style/layout primitives

## WiFi backend (planned)

The backend should detect which subsystem is available and use it:

1. **NetworkManager** (`nmcli`) — most common on desktop distros
2. **iwd / iwctl** — lightweight alternative (Arch, some embedded)
3. **wpa_supplicant** — fallback for minimal systems

Avoid shelling out where a D-Bus or netlink interface exists; shell-out helpers are acceptable for the initial prototype.

## Coding conventions

- No `print()` — use `log/slog` or `log` for any diagnostic output
- Keep platform-specific code in build-tagged files (`_linux.go`, `_darwin.go`) so the project continues to compile on macOS even as Linux-only backends are added
- Prefer small, focused functions; bubbletea `Update` handlers should delegate to helpers rather than growing large switch blocks
