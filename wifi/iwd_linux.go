//go:build linux

package wifi

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// iwdBackend implements Backend using iwd's iwctl CLI.
type iwdBackend struct {
	bin string // absolute path to iwctl

	mu    sync.Mutex
	iface string // cached wifi interface name
}

// Scan triggers a fresh scan, waits for it to complete, then returns all
// visible networks with Known/Connected populated.
func (b *iwdBackend) Scan() ([]Network, error) {
	iface, err := b.wifiInterface()
	if err != nil {
		return nil, err
	}

	// Trigger a scan. iwctl returns immediately; the scan runs asynchronously.
	_ = exec.Command(b.bin, "station", iface, "scan").Run()
	// Wait for the scan to finish. iwd typically takes 1–2 s; 3 s is conservative.
	time.Sleep(3 * time.Second)

	out, err := exec.Command(b.bin, "station", iface, "get-networks").Output()
	if err != nil {
		return nil, fmt.Errorf("iwctl get-networks: %w", err)
	}

	known, err := b.knownSSIDs()
	if err != nil {
		slog.Warn("could not read iwd network store", "err", err)
		known = map[string]bool{}
	}

	status, _ := b.Status()

	var networks []Network
	dashCount := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		// iwctl table output has two "---" separator lines before data rows.
		if strings.HasPrefix(trimmed, "---") {
			dashCount++
			continue
		}
		if dashCount < 2 {
			continue // still in title / column-header section
		}
		if trimmed == "" {
			continue
		}

		parts := splitIwctlRow(trimmed)
		if len(parts) < 3 {
			continue
		}
		ssid := parts[0]
		security := parts[1] // "psk", "open", "8021x"
		stars := parts[2]    // "****", "***", "**", "*"

		authType := iwdAuthType(security)
		secured := security != "open"
		connected := status.Connected && status.SSID == ssid

		networks = append(networks, Network{
			SSID:      ssid,
			Signal:    starsToDBm(stars),
			AuthType:  authType,
			Secured:   secured,
			Known:     known[ssid] || connected,
			Connected: connected,
		})
	}
	return networks, sc.Err()
}

// Connect associates with ssid, using passphrase if supplied.
func (b *iwdBackend) Connect(ssid, passphrase string) error {
	iface, err := b.wifiInterface()
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	if passphrase != "" {
		// --passphrase is supported in iwd ≥ 1.0 for non-interactive use.
		cmd = exec.Command(b.bin, "--passphrase", passphrase, "station", iface, "connect", ssid)
	} else {
		cmd = exec.Command(b.bin, "station", iface, "connect", ssid)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		slog.Error("iwctl connect failed", "ssid", ssid, "output", msg)
		return fmt.Errorf("%w: %s", err, msg)
	}
	slog.Info("connected", "ssid", ssid)
	return nil
}

// Disconnect drops the current connection.
func (b *iwdBackend) Disconnect() error {
	iface, err := b.wifiInterface()
	if err != nil {
		return err
	}
	out, err := exec.Command(b.bin, "station", iface, "disconnect").CombinedOutput()
	if err != nil {
		return fmt.Errorf("iwctl disconnect: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Status returns the current connection state by parsing `iwctl station show`.
func (b *iwdBackend) Status() (ConnectionStatus, error) {
	iface, err := b.wifiInterface()
	if err != nil {
		return ConnectionStatus{}, err
	}

	out, err := exec.Command(b.bin, "station", iface, "show").Output()
	if err != nil {
		return ConnectionStatus{}, fmt.Errorf("iwctl station show: %w", err)
	}

	var cs ConnectionStatus
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		parts := splitIwctlRow(strings.TrimSpace(sc.Text()))
		if len(parts) < 2 {
			continue
		}
		switch parts[0] {
		case "State":
			cs.Connected = parts[1] == "connected"
		case "Connected network":
			cs.SSID = parts[1]
		}
	}
	if cs.Connected {
		cs.IPAddress, cs.Gateway, cs.DNS = ifaceNetInfo(iface)
	}
	return cs, nil
}

// knownSSIDs returns SSIDs for which iwd has stored credentials.
// iwd stores one file per network under /var/lib/iwd/:
//
//	<SSID>.psk   — WPA2/WPA3-Personal
//	<SSID>.open  — open network (captive portals, etc.)
//	<SSID>.8021x — Enterprise (not in our scope, but detected as known)
func (b *iwdBackend) knownSSIDs() (map[string]bool, error) {
	const dir = "/var/lib/iwd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	known := make(map[string]bool)
	for _, e := range entries {
		for _, ext := range []string{".psk", ".open", ".8021x"} {
			if strings.HasSuffix(e.Name(), ext) {
				known[strings.TrimSuffix(e.Name(), ext)] = true
			}
		}
	}
	return known, nil
}

// wifiInterface returns the first WiFi interface iwd manages.
// The result is cached after the first successful call.
func (b *iwdBackend) wifiInterface() (string, error) {
	b.mu.Lock()
	cached := b.iface
	b.mu.Unlock()
	if cached != "" {
		return cached, nil
	}

	out, err := exec.Command(b.bin, "device", "list").Output()
	if err != nil {
		return "", fmt.Errorf("iwctl device list: %w", err)
	}

	dashCount := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "---") {
			dashCount++
			continue
		}
		if dashCount < 2 || line == "" {
			continue
		}
		parts := splitIwctlRow(line)
		if len(parts) >= 1 && parts[0] != "" && parts[0] != "Name" {
			b.mu.Lock()
			b.iface = parts[0]
			b.mu.Unlock()
			slog.Debug("iwd interface", "name", parts[0])
			return parts[0], nil
		}
	}
	return "", errors.New("no WiFi interface found via iwctl device list")
}

// splitIwctlRow splits a trimmed iwctl table row on runs of 2+ spaces.
// This handles SSIDs and property names that contain single spaces.
func splitIwctlRow(line string) []string {
	var parts []string
	var cur strings.Builder
	spaceRun := 0

	for _, r := range line {
		if r == ' ' {
			spaceRun++
		} else {
			if spaceRun >= 2 && cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			} else if spaceRun == 1 && cur.Len() > 0 {
				cur.WriteRune(' ')
			}
			spaceRun = 0
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// iwdAuthType maps iwd's security field to a canonical auth string.
func iwdAuthType(security string) string {
	switch strings.TrimSpace(security) {
	case "psk":
		return "WPA2" // iwd uses psk for both WPA2 and WPA3; best guess
	case "open":
		return "Open"
	case "8021x":
		return "WPA2-Ent"
	default:
		if security != "" {
			return security
		}
		return "Open"
	}
}

// starsToDBm converts iwctl's 1–4 star signal display to approximate dBm.
func starsToDBm(stars string) int {
	switch len(strings.TrimSpace(stars)) {
	case 4:
		return -50
	case 3:
		return -65
	case 2:
		return -75
	case 1:
		return -85
	default:
		return -90
	}
}
