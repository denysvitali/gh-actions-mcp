//go:build darwin

package config

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/keybase/go-keychain"
)

// getTokenFromKeychain attempts to get the GitHub token from the macOS keychain
func getTokenFromKeychain() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("keychain is only available on macOS")
	}

	// gh CLI stores the token as an Internet password with server "github.com"
	query := keychain.NewItem()
	query.SetSecClass(keychain.SecClassInternetPassword)
	query.SetServer("github.com")
	query.SetMatchLimit(keychain.MatchLimitOne)
	query.SetReturnData(true)

	results, err := keychain.QueryItem(query)
	if err != nil {
		return "", fmt.Errorf("failed to query keychain: %w", err)
	}

	if len(results) == 0 {
		return "", fmt.Errorf("no GitHub token found in keychain")
	}

	token := string(results[0].Data)
	if token == "" {
		return "", fmt.Errorf("token found but empty in keychain")
	}

	// Verify it looks like a GitHub token (gho_ = OAuth, ghp_ = PAT, ghs_ = server-to-server)
	if !strings.HasPrefix(token, "gho_") && !strings.HasPrefix(token, "ghp_") && !strings.HasPrefix(token, "ghs_") {
		log.Debugf("Warning: token from keychain doesn't have expected prefix (gho_/ghp_/ghs_)")
	}

	return token, nil
}
