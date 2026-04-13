//go:build linux

package wifi

import (
	"bufio"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// wpaBackend implements Backend using wpa_supplicant's wpa_cli tool.
// It communicates through the Unix-domain control socket that wpa_supplicant
// creates at startup (typically /run/wpa_supplicant/<iface>).
type wpaBackend struct {
	bin     string // absolute path to wpa_cli
	ctrlDir string // directory containing the control socket (e.g. /run/wpa_supplicant)
	iface   string // wireless interface name (e.g. wlan0)
}

// cli runs wpa_cli with the given arguments and returns trimmed stdout.
// Uses -p (ctrl socket dir) and -i (interface) so every call is unambiguous.
func (b *wpaBackend) cli(args ...string) (string, error) {
	cmdArgs := append([]string{"-p", b.ctrlDir, "-i", b.iface}, args...)
	out, err := exec.Command(b.bin, cmdArgs...).Output()
	return strings.TrimSpace(string(out)), err
}

// Scan triggers a fresh scan, waits for results, and returns deduplicated
// networks with Known/Connected populated.
func (b *wpaBackend) Scan() ([]Network, error) {
	// Trigger scan; ignore error (might already be scanning).
	_, _ = b.cli("scan")
	// wpa_supplicant scans asynchronously; 3 s is conservative.
	time.Sleep(3 * time.Second)

	out, err := b.cli("scan_results")
	if err != nil {
		return nil, fmt.Errorf("wpa_cli scan_results: %w", err)
	}

	known, err := b.knownSSIDs()
	if err != nil {
		slog.Warn("could not load known networks", "err", err)
		known = map[string]string{}
	}
	status, _ := b.Status()

	// seen maps SSID → index in networks for deduplication (keep strongest signal).
	seen := map[string]int{}
	var networks []Network

	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		// Skip header and status lines.
		if line == "" || strings.HasPrefix(line, "Selected interface") || strings.HasPrefix(line, "bssid") {
			continue
		}
		// Format: BSSID \t freq \t signal(dBm) \t flags \t SSID
		fields := strings.SplitN(line, "\t", 5)
		if len(fields) < 5 {
			slog.Debug("wpa scan_results: skipping malformed line", "line", line)
			continue
		}
		bssid := strings.TrimSpace(fields[0])
		freq, _ := strconv.Atoi(strings.TrimSpace(fields[1]))
		signal, _ := strconv.Atoi(strings.TrimSpace(fields[2]))
		flags := fields[3]
		ssid := fields[4]
		if ssid == "" {
			continue // hidden network
		}

		authType := parseWpaFlags(flags)
		secured := authType != "Open"
		connected := status.Connected && status.SSID == ssid
		_, isKnown := known[ssid]

		if idx, exists := seen[ssid]; exists {
			if signal > networks[idx].Signal {
				networks[idx].Signal = signal
				networks[idx].BSSID = bssid
				networks[idx].Frequency = freq
			}
			continue
		}
		seen[ssid] = len(networks)
		networks = append(networks, Network{
			SSID:      ssid,
			BSSID:     bssid,
			Signal:    signal,
			Frequency: freq,
			AuthType:  authType,
			Secured:   secured,
			Known:     isKnown || connected,
			Connected: connected,
		})
	}
	return networks, sc.Err()
}

// Connect associates with ssid. If the network is already known, selects it
// by ID; otherwise adds and configures a new network entry.
// The passphrase is intentionally never passed to slog.
func (b *wpaBackend) Connect(ssid, passphrase string) error {
	known, _ := b.knownSSIDs()

	if netID, ok := known[ssid]; ok {
		out, err := b.cli("select_network", netID)
		if err == nil && strings.Contains(out, "OK") {
			slog.Info("selecting known wpa network", "ssid", ssid, "id", netID)
			return b.waitConnected()
		}
		slog.Warn("select_network failed, re-adding network", "ssid", ssid, "err", err)
	}

	// Add a new network entry; wpa_cli returns the numeric ID.
	out, err := b.cli("add_network")
	if err != nil {
		return fmt.Errorf("wpa_cli add_network: %w", err)
	}
	// Output may be prefixed with "Selected interface '<iface>'\n"; take last line.
	netID := lastLine(out)

	// Set SSID — wpa_cli expects a quoted string as the value.
	if _, err := b.cli("set_network", netID, "ssid", `"`+ssid+`"`); err != nil {
		return fmt.Errorf("set_network ssid: %w", err)
	}

	if passphrase != "" {
		if _, err := b.cli("set_network", netID, "psk", `"`+passphrase+`"`); err != nil {
			return fmt.Errorf("set_network psk: %w", err)
		}
	} else {
		if _, err := b.cli("set_network", netID, "key_mgmt", "NONE"); err != nil {
			return fmt.Errorf("set_network key_mgmt: %w", err)
		}
	}

	if _, err := b.cli("enable_network", netID); err != nil {
		return fmt.Errorf("enable_network: %w", err)
	}
	if _, err := b.cli("select_network", netID); err != nil {
		return fmt.Errorf("select_network: %w", err)
	}
	// save_config requires update_config=1 in wpa_supplicant.conf; best-effort.
	if out, err := b.cli("save_config"); err != nil || strings.Contains(out, "FAIL") {
		slog.Warn("save_config failed — credentials not persisted across reboots", "out", out, "err", err)
	}

	return b.waitConnected()
}

// Disconnect drops the current connection on the interface.
func (b *wpaBackend) Disconnect() error {
	out, err := b.cli("disconnect")
	if err != nil {
		return fmt.Errorf("wpa_cli disconnect: %w", err)
	}
	if strings.Contains(out, "FAIL") {
		return fmt.Errorf("wpa_cli disconnect returned: %s", out)
	}
	return nil
}

// Status returns the current connection state by parsing `wpa_cli status`.
func (b *wpaBackend) Status() (ConnectionStatus, error) {
	out, err := b.cli("status")
	if err != nil {
		return ConnectionStatus{}, fmt.Errorf("wpa_cli status: %w", err)
	}
	var cs ConnectionStatus
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "wpa_state":
			cs.Connected = v == "COMPLETED"
		case "ssid":
			cs.SSID = v
		case "ip_address":
			cs.IPAddress = v
		}
	}
	return cs, nil
}

// knownSSIDs returns a map of SSID → wpa_supplicant network ID for all saved
// networks reported by `wpa_cli list_networks`.
func (b *wpaBackend) knownSSIDs() (map[string]string, error) {
	out, err := b.cli("list_networks")
	if err != nil {
		return nil, fmt.Errorf("wpa_cli list_networks: %w", err)
	}
	known := make(map[string]string)
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "network id") || strings.HasPrefix(line, "Selected") {
			continue
		}
		// Format: id \t ssid \t bssid \t flags
		fields := strings.SplitN(line, "\t", 4)
		if len(fields) >= 2 {
			known[fields[1]] = fields[0]
		}
	}
	return known, sc.Err()
}

// waitConnected polls wpa_supplicant status until wpa_state=COMPLETED or
// a 15-second timeout expires.
func (b *wpaBackend) waitConnected() error {
	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		status, err := b.Status()
		if err == nil && status.Connected {
			slog.Info("connected", "ssid", status.SSID)
			return nil
		}
	}
	return fmt.Errorf("timed out waiting for wpa_supplicant to reach COMPLETED state")
}

// parseWpaFlags extracts a human-readable auth type from wpa_supplicant
// capability flags like "[WPA2-PSK+SAE-CCMP][ESS]".
func parseWpaFlags(flags string) string {
	hasWPA2 := strings.Contains(flags, "WPA2-PSK") || strings.Contains(flags, "RSN-PSK")
	hasWPA3 := strings.Contains(flags, "SAE")
	hasWEP := strings.Contains(flags, "WEP")
	switch {
	case hasWPA2 && hasWPA3:
		return "WPA2/WPA3"
	case hasWPA3:
		return "WPA3"
	case hasWPA2:
		return "WPA2"
	case hasWEP:
		return "WEP"
	default:
		return "Open"
	}
}

// lastLine returns the last non-empty line from s.
// Used to extract the numeric network ID from wpa_cli add_network output, which
// may include a "Selected interface '...'" prefix line.
func lastLine(s string) string {
	var last string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			last = t
		}
	}
	return last
}
