package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

func projectRuntimeCachePath(repoRoot, namespace, filename string) (string, error) {
	if repoRoot == "" {
		return "", fmt.Errorf("repo root is empty")
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(repoRoot)))
	key := hex.EncodeToString(sum[:])[:16]
	return filepath.Join(root, "wipnote", namespace, key, filename), nil
}
