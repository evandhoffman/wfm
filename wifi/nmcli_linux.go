//go:build linux

package wifi

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

// nmcliBackend implements Backend using NetworkManager's nmcli CLI.
// It is the v1 implementation; a native D-Bus backend may replace it later
// without any changes to the Backend interface or the TUI.
type nmcliBackend struct {
	bin string // absolute path to nmcli binary
}

// Scan returns all visible access points, deduplicated by SSID (strongest
// signal wins), with Known and Connected populated.
func (b *nmcliBackend) Scan() ([]Network, error) {
	// Ask NM to do a fresh scan; ignore errors (e.g. already scanning).
	_ = exec.Command(b.bin, "dev", "wifi", "rescan").Run()

	out, err := exec.Command(b.bin,
		"--terse",
		"--fields", "IN-USE,BSSID,SSID,SIGNAL,FREQ,SECURITY",
		"dev", "wifi", "list",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("nmcli dev wifi list: %w", err)
	}

	known, err := b.knownSSIDs()
	if err != nil {
		slog.Warn("could not load saved connections", "err", err)
		known = map[string]bool{}
	}

	// seen maps SSID → index in networks; store best (active or strongest) entry.
	seen := map[string]int{}
	var networks []Network

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		// Fields: IN-USE, BSSID, SSID, SIGNAL, FREQ, SECURITY
		fields := splitTerse(line, 6)
		if len(fields) < 6 {
			slog.Debug("skipping malformed nmcli line", "line", line)
			continue
		}

		inUse := strings.TrimSpace(fields[0]) == "*"
		bssid := strings.TrimSpace(fields[1])
		ssid := fields[2]
		if ssid == "" || ssid == "--" {
			continue // hidden network
		}

		quality, _ := strconv.Atoi(strings.TrimSpace(fields[3]))
		signal := qualityToDBm(quality)
		freq := parseNmcliFreq(fields[4])
		security := strings.TrimSpace(fields[5])
		authType := parseNmcliSecurity(security)
		secured := authType != "Open"

		if idx, exists := seen[ssid]; exists {
			// Prefer the active entry; otherwise keep strongest signal.
			if inUse || signal > networks[idx].Signal {
				networks[idx].Signal = signal
				networks[idx].BSSID = bssid
				networks[idx].Frequency = freq
				networks[idx].Connected = inUse
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
			Known:     known[ssid] || inUse,
			Connected: inUse,
		})
	}
	return networks, sc.Err()
}

// Connect associates with ssid. If passphrase is empty the backend uses
// already-stored credentials (caller must ensure Known == true or Secured == false).
// The passphrase is intentionally never passed to slog.
func (b *nmcliBackend) Connect(ssid, passphrase string) error {
	args := []string{"--wait", "30", "dev", "wifi", "connect", ssid}
	if passphrase != "" {
		args = append(args, "password", passphrase)
	}
	out, err := exec.Command(b.bin, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		slog.Error("connect failed", "ssid", ssid, "output", msg)
		return fmt.Errorf("%w: %s", err, msg)
	}
	slog.Info("connected", "ssid", ssid)
	return nil
}

// Disconnect deactivates the WiFi interface.
func (b *nmcliBackend) Disconnect() error {
	iface, err := b.wifiInterface()
	if err != nil {
		return err
	}
	out, err := exec.Command(b.bin, "dev", "disconnect", iface).CombinedOutput()
	if err != nil {
		return fmt.Errorf("disconnect %s: %w: %s", iface, err, strings.TrimSpace(string(out)))
	}
	slog.Info("disconnected", "interface", iface)
	return nil
}

// Status returns the current connection state of the WiFi interface.
func (b *nmcliBackend) Status() (ConnectionStatus, error) {
	out, err := exec.Command(b.bin, "--terse", "--fields", "STATE", "general").Output()
	if err != nil {
		return ConnectionStatus{}, fmt.Errorf("nmcli general: %w", err)
	}
	if strings.TrimSpace(string(out)) != "connected" {
		return ConnectionStatus{}, nil
	}

	out2, err := exec.Command(b.bin,
		"--terse", "--fields", "IN-USE,SSID",
		"dev", "wifi",
	).Output()
	if err != nil {
		return ConnectionStatus{Connected: true}, nil
	}

	sc := bufio.NewScanner(bytes.NewReader(out2))
	for sc.Scan() {
		fields := splitTerse(sc.Text(), 2)
		if len(fields) == 2 && strings.TrimSpace(fields[0]) == "*" {
			cs := ConnectionStatus{Connected: true, SSID: fields[1]}
			if iface, err := b.wifiInterface(); err == nil {
				cs.IPAddress, cs.Gateway, cs.DNS = ifaceNetInfo(iface)
			}
			return cs, nil
		}
	}
	return ConnectionStatus{Connected: true}, nil
}

// knownSSIDs returns the set of SSIDs for which NM has saved credentials.
// NM uses the connection name as the SSID by default; this is correct for
// the common case. A D-Bus backend can do this more precisely if needed.
func (b *nmcliBackend) knownSSIDs() (map[string]bool, error) {
	out, err := exec.Command(b.bin,
		"--terse", "--fields", "NAME,TYPE",
		"connection", "show",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("nmcli connection show: %w", err)
	}

	known := make(map[string]bool)
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := splitTerse(sc.Text(), 2)
		if len(fields) == 2 && fields[1] == "802-11-wireless" {
			known[fields[0]] = true
		}
	}
	return known, sc.Err()
}

// wifiInterface returns the device name of the first WiFi interface NM knows about.
func (b *nmcliBackend) wifiInterface() (string, error) {
	out, err := exec.Command(b.bin,
		"--terse", "--fields", "DEVICE,TYPE",
		"device",
	).Output()
	if err != nil {
		return "", fmt.Errorf("nmcli device: %w", err)
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := splitTerse(sc.Text(), 2)
		if len(fields) == 2 && fields[1] == "wifi" {
			return fields[0], nil
		}
	}
	return "", errors.New("no WiFi interface found")
}

// splitTerse splits a terse nmcli output line on ':' into at most n fields,
// honouring '\:' escape sequences for literal colons embedded in values.
func splitTerse(line string, n int) []string {
	fields := make([]string, 0, n)
	var cur strings.Builder

	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		switch {
		case runes[i] == '\\' && i+1 < len(runes) && runes[i+1] == ':':
			cur.WriteRune(':')
			i++ // consume the escaped colon
		case runes[i] == ':' && len(fields) < n-1:
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(runes[i])
		}
	}
	return append(fields, cur.String())
}

// parseNmcliFreq parses nmcli's FREQ field, which looks like "2437 MHz" or
// "5745 MHz", and returns the integer MHz value.
func parseNmcliFreq(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "--" {
		return 0
	}
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return 0
	}
	// Handle both integer and float representations (e.g. "5745" or "5745.0").
	f, _ := strconv.ParseFloat(parts[0], 64)
	return int(f)
}

// parseNmcliSecurity maps nmcli's SECURITY field to a canonical auth string.
// nmcli may report values like "WPA2", "WPA1 WPA2", "WPA3", "WPA2 WPA3", "--".
func parseNmcliSecurity(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "--" {
		return "Open"
	}
	hasWPA1 := strings.Contains(s, "WPA1")
	hasWPA2 := strings.Contains(s, "WPA2")
	hasWPA3 := strings.Contains(s, "WPA3")
	switch {
	case hasWPA2 && hasWPA3:
		return "WPA2/WPA3"
	case hasWPA1 && hasWPA2:
		return "WPA/WPA2"
	case hasWPA3:
		return "WPA3"
	case hasWPA2:
		return "WPA2"
	case hasWPA1:
		return "WPA"
	default:
		return s // WEP, OWE, etc. — pass through
	}
}

// qualityToDBm converts nmcli's 0-100 signal-quality percentage to
// approximate dBm using the standard Linux formula: dBm = (q/2) - 100.
func qualityToDBm(quality int) int {
	if quality <= 0 {
		return -100
	}
	if quality >= 100 {
		return -50
	}
	return (quality / 2) - 100
}
