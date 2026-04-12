//go:build darwin

package wifi

import "errors"

// Detect is a compile-time stub for macOS.
// wfm only manages WiFi on Linux; this file exists so the package
// (and main.go) continue to build on the developer's Mac.
func Detect() (Backend, error) {
	return nil, errors.New("wfm only manages WiFi on Linux")
}
