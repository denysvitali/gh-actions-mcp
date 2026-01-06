//go:build darwin && !cgo

package config

import "fmt"

// getTokenFromKeychain attempts to get the GitHub token from the macOS keychain
func getTokenFromKeychain() (string, error) {
	return "", fmt.Errorf("keychain access requires CGO to be enabled on macOS")
}
