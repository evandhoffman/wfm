# Issue #13 — Forget Network: work in progress

## What's done

All backend changes are complete and compiling.

**`wifi/backend.go`**
- Added `Forget(ssid string) error` to the `Backend` interface

**`wifi/nmcli_linux.go`**
- `Forget`: runs `nmcli connection delete <ssid>`

**`wifi/iwd_linux.go`**
- Added `"path/filepath"` import
- `Forget`: deletes `/var/lib/iwd/<ssid>.{psk,open,8021x}`

**`wifi/wpasupplicant_linux.go`**
- `Forget`: `wpa_cli remove_network <id>` + `wpa_cli save_config`

**`main.go`** (partial)
- Added `stateConfirmForget` to the state machine constants
- Added `forgetResultMsg{ssid, err}` to the tea message types
- Added `forgetCmd(b, ssid)` tea command
- `f` key in `stateList` → sets `m.selected`, transitions to `stateConfirmForget` (only fires if `n.Known == true`)
- `stateConfirmForget` key handler: `y`/`Y` → `forgetCmd` + `logCmd`; `n`/`N`/`esc`/`ctrl+c` → back to `stateList`
- `forgetResultMsg` handler in `Update`: success → log + rescan; error → `stateError` + log

## What's NOT done yet (interrupted mid-edit)

All remaining work is in `main.go`, view layer only:

1. **Refactor `rescanOverlayView`** into a shared helper:
   ```go
   func (m model) overlayOnList(dialog string) string { ... }
   ```
   Move all the fade/stamp logic there. Then `rescanOverlayView` becomes a one-liner that builds its dialog string and calls `overlayOnList`.

2. **Add `confirmForgetView()`**:
   ```go
   func (m model) confirmForgetView() string {
       dialog := lipgloss.NewStyle().
           Border(lipgloss.RoundedBorder()).
           BorderForeground(lipgloss.Color("9")). // red = destructive
           Padding(1, 4).
           Render(fmt.Sprintf(
               "Forget %q?\n\n%s",
               m.selected.SSID,
               subtleStyle.Render("y: forget  •  n / esc: cancel"),
           ))
       return m.overlayOnList(dialog)
   }
   ```

3. **Add `stateConfirmForget` case in `View()`**:
   ```go
   case stateConfirmForget:
       return m.confirmForgetView()
   ```

4. **Update the footer string** in `stateList` (and in `rescanOverlayView`'s background) to include `f: forget`.

5. **Build and verify** compiles clean, then commit and push.
