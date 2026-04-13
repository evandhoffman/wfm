//go:build linux

package wifi

import (
	"errors"
	"log/slog"
	"os/exec"
)

// Detect probes the running system and returns the best available Backend.
// It checks for required binaries upfront and returns a descriptive error
// if no supported WiFi subsystem is found.
//
// Detection order:
//  1. NetworkManager (nmcli) — most common on Ubuntu, Fedora, Debian, etc.
//  2. iwd (iwctl)            — Arch Linux and minimal systems (TODO)
func Detect() (Backend, error) {
	if nmcliPath, err := exec.LookPath("nmcli"); err == nil {
		slog.Debug("found nmcli", "path", nmcliPath)
		if isNMRunning(nmcliPath) {
			slog.Info("using NetworkManager backend", "nmcli", nmcliPath)
			return &nmcliBackend{bin: nmcliPath}, nil
		}
		slog.Warn("nmcli present but NetworkManager is not running")
	}

	// TODO: iwd/iwctl backend
	// if iwctlPath, err := exec.LookPath("iwctl"); err == nil {
	//     slog.Info("using iwd backend", "iwctl", iwctlPath)
	//     return &iwdBackend{bin: iwctlPath}, nil
	// }

	return nil, errors.New(
		"no supported WiFi backend found\n" +
			"  • NetworkManager not running — try: sudo systemctl start NetworkManager\n" +
			"  • iwd support coming soon",
	)
}

// isNMRunning checks that the NetworkManager daemon is reachable by running
// `nmcli -t general status`, which exits non-zero if NM is not running.
func isNMRunning(nmcliPath string) bool {
	err := exec.Command(nmcliPath, "-t", "general", "status").Run()
	return err == nil
}
