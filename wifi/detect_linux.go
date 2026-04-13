//go:build linux

package wifi

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ifaceLinkSpeed returns the negotiated TX link rate for iface by parsing
// `iw dev <iface> link`. Returns an empty string if iw is unavailable or the
// interface is not associated.
func ifaceLinkSpeed(iface string) string {
	out, err := exec.Command("iw", "dev", iface, "link").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "tx bitrate:") {
			// "tx bitrate: 866.7 MBit/s VHT-MCS 9 80MHz VHT-NSS 2"
			parts := strings.Fields(strings.TrimPrefix(line, "tx bitrate:"))
			if len(parts) >= 2 {
				return parts[0] + " " + parts[1]
			}
		}
	}
	return ""
}

// ifaceNetInfo reads IPv4 address (CIDR), default gateway, and DNS servers for
// iface by shelling out to standard Linux tools. All fields are best-effort;
// empty string means "not available".
func ifaceNetInfo(iface string) (ipAddr, gateway, dns string) {
	// IPv4 address from `ip -4 addr show <iface>`.
	if out, err := exec.Command("ip", "-4", "addr", "show", iface).Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "inet ") {
				if parts := strings.Fields(line); len(parts) >= 2 {
					ipAddr = parts[1] // e.g. "192.168.1.100/24"
				}
				break
			}
		}
	}

	// Default gateway from `ip -4 route show dev <iface>`.
	if out, err := exec.Command("ip", "-4", "route", "show", "dev", iface).Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "default via ") {
				if parts := strings.Fields(line); len(parts) >= 3 {
					gateway = parts[2]
				}
				break
			}
		}
	}

	// DNS: prefer per-interface info from resolvectl, fall back to resolv.conf.
	dns = ifaceDNS(iface)
	return
}

// ifaceDNS returns space-separated DNS servers for iface.
func ifaceDNS(iface string) string {
	// resolvectl dns <iface> emits lines like "Link 3 (wlan0): 1.1.1.1 1.0.0.1"
	if out, err := exec.Command("resolvectl", "dns", iface).Output(); err == nil {
		line := strings.TrimSpace(string(out))
		if idx := strings.Index(line, ":"); idx >= 0 {
			servers := strings.TrimSpace(line[idx+1:])
			if servers != "" {
				return servers
			}
		}
	}
	// Fall back to nameserver lines in /etc/resolv.conf.
	if data, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		var servers []string
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "nameserver ") {
				if parts := strings.Fields(line); len(parts) >= 2 {
					servers = append(servers, parts[1])
				}
			}
		}
		return strings.Join(servers, " ")
	}
	return ""
}

// Detect probes the running system and returns the best available Backend.
// It checks for required binaries upfront and returns a descriptive error
// if no supported WiFi subsystem is found.
//
// Detection order:
//  1. NetworkManager (nmcli) — most common on Ubuntu, Fedora, Debian, etc.
//  2. iwd (iwctl)            — Arch Linux, some minimal systems
//  3. wpa_supplicant (wpa_cli) — Raspberry Pi OS, Debian, Ubuntu minimal
func Detect() (Backend, error) {
	if nmcliPath, err := exec.LookPath("nmcli"); err == nil {
		slog.Debug("found nmcli", "path", nmcliPath)
		if isNMRunning(nmcliPath) {
			slog.Info("using NetworkManager backend", "nmcli", nmcliPath)
			return &nmcliBackend{bin: nmcliPath}, nil
		}
		slog.Warn("nmcli present but NetworkManager is not running")
	}

	if iwctlPath, err := exec.LookPath("iwctl"); err == nil {
		slog.Debug("found iwctl", "path", iwctlPath)
		if isIWDRunning(iwctlPath) {
			slog.Info("using iwd backend", "iwctl", iwctlPath)
			return &iwdBackend{bin: iwctlPath}, nil
		}
		slog.Warn("iwctl present but iwd is not running")
	}

	if wpacliPath, err := exec.LookPath("wpa_cli"); err == nil {
		slog.Debug("found wpa_cli", "path", wpacliPath)
		if ctrlDir, iface, ok := wpaCtrlSocket(wpacliPath); ok {
			slog.Info("using wpa_supplicant backend", "wpa_cli", wpacliPath, "iface", iface, "ctrl_dir", ctrlDir)
			return &wpaBackend{bin: wpacliPath, ctrlDir: ctrlDir, iface: iface}, nil
		}
		slog.Warn("wpa_cli present but no responsive control socket found")
	}

	return nil, errors.New(
		"no supported WiFi backend found\n" +
			"  • NetworkManager: sudo systemctl start NetworkManager\n" +
			"  • iwd:            sudo systemctl start iwd\n" +
			"  • wpa_supplicant: sudo systemctl start wpa_supplicant@<iface>",
	)
}

// isNMRunning checks that the NetworkManager daemon is reachable by running
// `nmcli -t general status`, which exits non-zero if NM is not running.
func isNMRunning(nmcliPath string) bool {
	return exec.Command(nmcliPath, "-t", "general", "status").Run() == nil
}

// isIWDRunning checks that the iwd daemon is reachable by asking iwctl to
// list devices; exits non-zero if iwd is not running.
func isIWDRunning(iwctlPath string) bool {
	return exec.Command(iwctlPath, "device", "list").Run() == nil
}

// wifiInterfaces returns the names of all wireless interfaces on the system
// by scanning /sys/class/net for entries that have a "wireless" subdirectory
// (set by the kernel for any 802.11 device).
func wifiInterfaces() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	var ifaces []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join("/sys/class/net", e.Name(), "wireless")); err == nil {
			ifaces = append(ifaces, e.Name())
		}
	}
	return ifaces
}

// wpaCtrlSocket searches for a live wpa_supplicant control socket by pinging
// each wireless interface in the common socket directories.
// Returns the socket directory and interface name on success.
func wpaCtrlSocket(wpacliPath string) (ctrlDir, iface string, ok bool) {
	for _, dir := range []string{"/run/wpa_supplicant", "/var/run/wpa_supplicant"} {
		for _, ifname := range wifiInterfaces() {
			out, err := exec.Command(wpacliPath, "-p", dir, "-i", ifname, "ping").Output()
			if err == nil && strings.Contains(string(out), "PONG") {
				return dir, ifname, true
			}
		}
	}
	return "", "", false
}
