// custody-key-check compares public-key fingerprints only. It is safe to run
// locally: no request is made and no key material is printed.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/custody"
)

const (
	privateKeyPath = "fireblocks_sandbox_private.key"
	csrPath        = "fireblocks_sandbox.csr"
)

type result struct {
	ConfiguredKeyFingerprint string `json:"configured_key_fingerprint"`
	KeyFileFingerprint       string `json:"key_file_fingerprint"`
	CSRFingerprint           string `json:"csr_fingerprint"`
	ConfiguredMatchesFile    bool   `json:"configured_matches_key_file"`
	ConfiguredMatchesCSR     bool   `json:"configured_matches_csr"`
	FileMatchesCSR           bool   `json:"key_file_matches_csr"`
}

func main() {
	cfg := config.Load()
	configured, err := custody.PublicKeyFingerprint([]byte(cfg.FireblocksPrivateKey))
	if err != nil {
		fatal(fmt.Errorf("configured Fireblocks private key: %w", err))
	}
	keyFile, err := os.ReadFile(privateKeyPath)
	if err != nil {
		fatal(err)
	}
	file, err := custody.PublicKeyFingerprint(keyFile)
	if err != nil {
		fatal(fmt.Errorf("%s: %w", privateKeyPath, err))
	}
	csr, err := os.ReadFile(csrPath)
	if err != nil {
		fatal(err)
	}
	csrFingerprint, err := custody.CSRPublicKeyFingerprint(csr)
	if err != nil {
		fatal(fmt.Errorf("%s: %w", csrPath, err))
	}
	_ = json.NewEncoder(os.Stdout).Encode(result{
		ConfiguredKeyFingerprint: configured,
		KeyFileFingerprint:       file,
		CSRFingerprint:           csrFingerprint,
		ConfiguredMatchesFile:    configured == file,
		ConfiguredMatchesCSR:     configured == csrFingerprint,
		FileMatchesCSR:           file == csrFingerprint,
	})
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "Fireblocks key check failed:", err)
	os.Exit(1)
}
