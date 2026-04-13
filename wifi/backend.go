// Package wifi provides a backend-agnostic interface for managing WiFi on Linux.
// Implementations live in *_linux.go files; detect_darwin.go provides a
// no-op stub so the package compiles on macOS during development.
package wifi

// Network represents a WiFi access point discovered during a scan.
type Network struct {
	SSID      string
	BSSID     string // MAC address of the strongest-signal AP; empty if unknown
	Signal    int    // dBm; e.g. -45 (strong) to -90 (very weak)
	Frequency int    // MHz; e.g. 2437, 5745; 0 if unknown
	AuthType  string // "WPA2", "WPA3", "WPA2/WPA3", "WEP", "Open"; empty if unknown
	Secured   bool   // requires a passphrase (WPA2/WPA3-Personal)
	Known     bool   // this system already has credentials stored for it
	Connected bool   // currently the active connection
}

// ConnectionStatus is the current state of the wireless interface.
type ConnectionStatus struct {
	Connected bool
	SSID      string
	IPAddress string // CIDR, e.g. "192.168.1.100/24"; empty if no IPv4
	Gateway   string // default gateway IPv4; empty if unknown
	DNS       string // space-separated nameservers; empty if unknown
}

// Backend is implemented by each WiFi subsystem (NetworkManager, iwd, …).
// All method implementations must be safe to call concurrently with the
// bubbletea event loop.
type Backend interface {
	// Scan returns all visible access points with Known/Connected populated.
	// The same SSID may be advertised by multiple BSSIDs; implementations
	// should deduplicate by SSID, keeping the strongest signal.
	Scan() ([]Network, error)

	// Connect associates with the given SSID.
	// Pass an empty passphrase for open networks or when Known == true
	// (the backend will use already-stored credentials).
	// Passphrase must never be logged.
	Connect(ssid, passphrase string) error

	// Disconnect drops the current connection on the WiFi interface.
	Disconnect() error

	// Status returns the current connection state of the WiFi interface.
	Status() (ConnectionStatus, error)
}
