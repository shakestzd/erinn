package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	githubReleaseAPI = "https://api.github.com/repos/shakestzd/wipnote/releases/latest"
	downloadBaseURL  = "https://github.com/shakestzd/wipnote/releases/download"
)

func upgradeCmd() *cobra.Command {
	var checkOnly bool
	var pinVersion string

	cmd := &cobra.Command{
		Use:     "upgrade",
		Aliases: []string{"update"},
		Short:   "Upgrade wipnote to the latest (or specified) version",
		Long:    "Download and install the latest wipnote release from GitHub. Use --check to preview without installing.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUpgrade(cmd.OutOrStdout(), checkOnly, pinVersion)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Show current vs latest version without installing")
	cmd.Flags().StringVar(&pinVersion, "version", "", "Install a specific version (e.g. 0.54.9)")
	return cmd
}

func runUpgrade(out io.Writer, checkOnly bool, pinVersion string) error {
	currentVer := strings.TrimPrefix(version, "v")

	// Determine target version.
	targetVer, err := resolveTargetVersion(pinVersion)
	if err != nil {
		return fmt.Errorf("resolving target version: %w", err)
	}

	if checkOnly {
		fmt.Fprintf(out, "current: %s\n", currentVer)
		fmt.Fprintf(out, "latest:  %s\n", targetVer)
		if targetVer == currentVer {
			fmt.Fprintln(out, "status:  up to date")
		} else {
			fmt.Fprintln(out, "status:  update available")
		}
		return nil
	}

	// Determine platform.
	goos, goarch := runtime.GOOS, runtime.GOARCH
	platformOS, platformArch, err := mapPlatform(goos, goarch)
	if err != nil {
		return err
	}

	archive := archiveName(targetVer, platformOS, platformArch)
	url := fmt.Sprintf("%s/v%s/%s", downloadBaseURL, targetVer, archive)

	fmt.Fprintf(out, "Downloading wipnote v%s for %s/%s...\n", targetVer, platformOS, platformArch)

	// Download to temp dir.
	tmpDir, err := os.MkdirTemp("", "wipnote-upgrade-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tarballPath := filepath.Join(tmpDir, archive)
	if err := downloadFile(url, tarballPath); err != nil {
		return fmt.Errorf("downloading release: %w", err)
	}

	// Verify SHA256 of the downloaded archive against the release's
	// checksums file. Fails the upgrade on mismatch — never install a
	// tampered or corrupted archive. If the checksums file cannot be
	// fetched (e.g. older release without one), the upgrade proceeds
	// with a warning so we never lock users out of older versions.
	//
	// TODO: add cosign keyless signature verification of the checksums
	// file once cosign Go SDK is acceptable as a dependency. For now,
	// `install.sh` performs the optional cosign step when available.
	checksumsURL := fmt.Sprintf("%s/v%s/wipnote_%s_checksums.txt", downloadBaseURL, targetVer, targetVer)
	if err := verifyArchiveChecksum(out, tarballPath, archive, checksumsURL); err != nil {
		return fmt.Errorf("verifying archive checksum: %w", err)
	}

	// Extract archive into an "extracted" subdir of the temp dir. This gives
	// us both the wipnote binary and the bundled plugin tree (since v0.59).
	extractRoot := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractRoot, 0o755); err != nil {
		return fmt.Errorf("creating extract dir: %w", err)
	}
	if err := extractArchive(tarballPath, extractRoot); err != nil {
		return fmt.Errorf("extracting archive: %w", err)
	}

	binaryName := "wipnote"
	extractedPath := filepath.Join(extractRoot, binaryName)
	if _, err := os.Stat(extractedPath); err != nil {
		return fmt.Errorf("binary %q not found in archive: %w", binaryName, err)
	}

	// Determine install destination.
	currentBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving current binary path: %w", err)
	}
	currentBin, err = filepath.EvalSymlinks(currentBin)
	if err != nil {
		return fmt.Errorf("resolving symlinks for current binary: %w", err)
	}

	// Check if destination is writable.
	installDir := filepath.Dir(currentBin)
	if err := checkWritable(installDir); err != nil {
		return fmt.Errorf("install directory %s is not writable: %w\nTry: sudo wipnote upgrade, or reinstall to ~/.local/bin", installDir, err)
	}

	fmt.Fprintf(out, "Installing to %s...\n", currentBin)

	// Atomic replace: try os.Rename; fall back to copy on cross-device.
	if err := atomicReplace(extractedPath, currentBin); err != nil {
		return fmt.Errorf("replacing binary: %w", err)
	}

	// Install the bundled plugin tree, if present, to keep the binary and
	// plugin assets in lockstep. Older release archives (< v0.59) had no
	// plugin/ subdirectory; in that case we silently skip.
	extractedPlugin := filepath.Join(extractRoot, "plugin")
	if info, err := os.Stat(extractedPlugin); err == nil && info.IsDir() {
		if err := installPluginTree(extractedPlugin); err != nil {
			fmt.Fprintf(out, "warning: failed to install bundled plugin tree: %v\n", err)
		} else {
			fmt.Fprintln(out, "Installed bundled plugin tree to ~/.local/share/wipnote/plugin")
		}
	}

	// Install the bundled Codex CLI marketplace tree, if present. Same Phase A
	// rationale: keep binary and harness assets in lockstep. Older archives
	// (< v0.59) had no codex-marketplace/ subdirectory; silently skip.
	extractedCodex := filepath.Join(extractRoot, "codex-marketplace")
	if info, err := os.Stat(extractedCodex); err == nil && info.IsDir() {
		if err := installCodexTree(extractedCodex); err != nil {
			fmt.Fprintf(out, "warning: failed to install bundled codex-marketplace tree: %v\n", err)
		} else {
			fmt.Fprintln(out, "Installed bundled codex-marketplace tree to ~/.local/share/wipnote/codex-marketplace")
		}
	}

	// Install the bundled Gemini CLI extension tree, if present. Same Phase A
	// rationale: keep binary and harness assets in lockstep. Older archives
	// (< v0.59) had no gemini-extension/ subdirectory; silently skip.
	extractedGemini := filepath.Join(extractRoot, "gemini-extension")
	if info, err := os.Stat(extractedGemini); err == nil && info.IsDir() {
		if err := installGeminiTree(extractedGemini); err != nil {
			fmt.Fprintf(out, "warning: failed to install bundled gemini-extension tree: %v\n", err)
		} else {
			fmt.Fprintln(out, "Installed bundled gemini-extension tree to ~/.local/share/wipnote/gemini-extension")
		}
	}

	// Install the bundled Antigravity extension tree, if present. Older
	// archives had no antigravity-extension/ subdirectory; silently skip.
	extractedAntigravity := filepath.Join(extractRoot, "antigravity-extension")
	if info, err := os.Stat(extractedAntigravity); err == nil && info.IsDir() {
		if err := installAntigravityTree(extractedAntigravity); err != nil {
			fmt.Fprintf(out, "warning: failed to install bundled antigravity-extension tree: %v\n", err)
		} else {
			fmt.Fprintln(out, "Installed bundled antigravity-extension tree to ~/.local/share/wipnote/antigravity-extension")
		}
	}

	// Update ~/.local/share/wipnote/.binary-version so bootstrap fast-path works.
	updateVersionFile(targetVer)

	// Self-test: run the newly installed binary's version subcommand.
	fmt.Fprintln(out, "Verifying installed binary...")
	if err := verifySelfVersion(currentBin, targetVer); err != nil {
		fmt.Fprintf(out, "warning: version verification failed: %v\n", err)
		fmt.Fprintln(out, "The binary was installed but may not report the expected version.")
	} else {
		fmt.Fprintf(out, "wipnote v%s installed successfully.\n", targetVer)
	}
	return nil
}

// resolveTargetVersion returns the pinned version if provided, otherwise
// fetches the latest tag from the GitHub releases API.
func resolveTargetVersion(pinVersion string) (string, error) {
	if pinVersion != "" {
		return strings.TrimPrefix(pinVersion, "v"), nil
	}
	return fetchUpgradeVersion()
}

// fetchUpgradeVersion queries the GitHub releases API and returns the latest tag.
func fetchUpgradeVersion() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(githubReleaseAPI)
	if err != nil {
		return "", fmt.Errorf("querying GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("parsing GitHub API response: %w", err)
	}
	if payload.TagName == "" {
		return "", fmt.Errorf("GitHub API returned empty tag_name")
	}
	return strings.TrimPrefix(payload.TagName, "v"), nil
}

// mapPlatform converts GOOS/GOARCH to the archive naming used by goreleaser.
func mapPlatform(goos, goarch string) (string, string, error) {
	var os, arch string
	switch goos {
	case "darwin":
		os = "darwin"
	case "linux":
		os = "linux"
	default:
		return "", "", fmt.Errorf("unsupported OS: %s", goos)
	}
	switch goarch {
	case "amd64":
		arch = "amd64"
	case "arm64":
		arch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported architecture: %s", goarch)
	}
	return os, arch, nil
}

// downloadFile fetches url and writes it to dest.
func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// extractArchive extracts a .tar.gz archive into destRoot, preserving the
// archive's internal directory layout. Used to lift out both the top-level
// wipnote binary and the bundled plugin/ tree (since v0.59) in a single pass.
//
// Hardens against path-traversal entries ("../") by rejecting any name that
// escapes destRoot. Regular files inherit the tar header's mode (with 0o600
// minimum so we can still read what we write). Directories are created with
// 0o755. Other entry types (symlinks, devices) are ignored — we control the
// archive and don't produce them.
func extractArchive(tarball, destRoot string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading gzip: %w", err)
	}
	defer gr.Close()

	absRoot, err := filepath.Abs(destRoot)
	if err != nil {
		return fmt.Errorf("resolving extract root: %w", err)
	}

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		// Skip entries with absolute paths or "../" traversal.
		clean := filepath.Clean(hdr.Name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
			continue
		}
		target := filepath.Join(absRoot, clean)
		// Defense in depth: confirm the resolved target is under destRoot.
		if !strings.HasPrefix(target+string(filepath.Separator), absRoot+string(filepath.Separator)) && target != absRoot {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("mkdir parent of %s: %w", target, err)
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			if mode == 0 {
				mode = 0o644
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("creating %s: %w", target, err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("writing %s: %w", target, err)
			}
			if err := out.Close(); err != nil {
				return fmt.Errorf("closing %s: %w", target, err)
			}
			if err := os.Chmod(target, mode); err != nil {
				return fmt.Errorf("chmod %s: %w", target, err)
			}
		default:
			// Symlinks/devices: not produced by goreleaser for this archive.
		}
	}
	return nil
}

// installPluginTree replaces ~/.local/share/wipnote/plugin atomically by
// moving srcPluginDir into place after removing any prior contents. Mirrors
// the bootstrap.sh logic so that upgrades via either path leave identical
// on-disk state.
func installPluginTree(srcPluginDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	metaDir := filepath.Join(home, ".local", "share", "wipnote")
	destPlugin := filepath.Join(metaDir, "plugin")

	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", metaDir, err)
	}
	if err := os.RemoveAll(destPlugin); err != nil {
		return fmt.Errorf("remove existing %s: %w", destPlugin, err)
	}
	// Try rename first (atomic on same filesystem). Fall back to recursive
	// copy on cross-device — temp dirs and ~/.local/share can live on
	// different volumes (e.g. inside containers).
	if err := os.Rename(srcPluginDir, destPlugin); err == nil {
		return nil
	}
	return copyDirRecursive(srcPluginDir, destPlugin)
}

// installCodexTree replaces ~/.local/share/wipnote/codex-marketplace atomically
// by moving srcCodexDir into place after removing any prior contents. Mirrors
// installPluginTree so the upgrade and bootstrap paths leave identical on-disk
// state across all three harness trees.
func installCodexTree(srcCodexDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	metaDir := filepath.Join(home, ".local", "share", "wipnote")
	destCodex := filepath.Join(metaDir, "codex-marketplace")

	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", metaDir, err)
	}
	if err := os.RemoveAll(destCodex); err != nil {
		return fmt.Errorf("remove existing %s: %w", destCodex, err)
	}
	if err := os.Rename(srcCodexDir, destCodex); err == nil {
		return nil
	}
	return copyDirRecursive(srcCodexDir, destCodex)
}

// installGeminiTree replaces ~/.local/share/wipnote/gemini-extension atomically
// by moving srcGeminiDir into place after removing any prior contents. Mirrors
// installPluginTree so the upgrade and bootstrap paths leave identical on-disk
// state across all three harness trees.
func installGeminiTree(srcGeminiDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	metaDir := filepath.Join(home, ".local", "share", "wipnote")
	destGemini := filepath.Join(metaDir, "gemini-extension")

	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", metaDir, err)
	}
	if err := os.RemoveAll(destGemini); err != nil {
		return fmt.Errorf("remove existing %s: %w", destGemini, err)
	}
	if err := os.Rename(srcGeminiDir, destGemini); err == nil {
		return nil
	}
	return copyDirRecursive(srcGeminiDir, destGemini)
}

// installAntigravityTree replaces ~/.local/share/wipnote/antigravity-extension
// atomically by moving srcAntigravityDir into place after removing any prior
// contents. Mirrors installPluginTree so upgrade and bootstrap paths leave
// identical on-disk state across all harness trees.
func installAntigravityTree(srcAntigravityDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	metaDir := filepath.Join(home, ".local", "share", "wipnote")
	destAntigravity := filepath.Join(metaDir, "antigravity-extension")

	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", metaDir, err)
	}
	if err := os.RemoveAll(destAntigravity); err != nil {
		return fmt.Errorf("remove existing %s: %w", destAntigravity, err)
	}
	if err := os.Rename(srcAntigravityDir, destAntigravity); err == nil {
		return nil
	}
	return copyDirRecursive(srcAntigravityDir, destAntigravity)
}

// copyDirRecursive walks src and copies every entry into dst, preserving file
// modes. Symlinks are skipped (the plugin tree doesn't contain any).
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		default:
			return nil
		}
	})
}

// atomicInstallBinary installs the binary at src to dest using the
// sibling-temp + rename pattern. A temp file is created in the same directory
// as dest (same filesystem), so os.Rename always succeeds without crossing a
// device boundary. Rename replaces the directory entry atomically: a running
// process keeps its old inode until exit, so this succeeds even when dest is a
// busy/executing binary (avoids ETXTBSY from in-place O_TRUNC writes).
func atomicInstallBinary(src, dest string) error {
	destDir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(destDir, ".wipnote-upgrade-*.tmp")
	if err != nil {
		return fmt.Errorf("creating sibling temp: %w", err)
	}
	tmpPath := tmp.Name()

	// Cleanup on any error so we never leave a .tmp turd behind.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	in, err := os.Open(src)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("opening source binary: %w", err)
	}
	defer in.Close()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temp binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing temp binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp binary: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("chmod temp binary: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return fmt.Errorf("renaming temp to dest: %w", err)
	}
	success = true
	return nil
}

// atomicReplace replaces dest with src using atomicInstallBinary (sibling-temp
// + rename), which avoids ETXTBSY when dest is a currently-running executable.
func atomicReplace(src, dest string) error {
	return atomicInstallBinary(src, dest)
}

// checkWritable verifies the directory is writable by attempting to create a
// temp file inside it.
func checkWritable(dir string) error {
	tmp, err := os.CreateTemp(dir, ".wipnote-write-test-*")
	if err != nil {
		return err
	}
	tmp.Close()
	return os.Remove(tmp.Name())
}

// updateVersionFile writes the installed version to ~/.local/share/wipnote/.binary-version.
func updateVersionFile(ver string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	versionFile := filepath.Join(home, ".local", "share", "wipnote", ".binary-version")
	_ = os.MkdirAll(filepath.Dir(versionFile), 0o755)
	_ = os.WriteFile(versionFile, []byte(ver), 0o644)
}

// archiveName constructs the archive filename matching goreleaser's name_template:
// "wipnote_{{.Version}}_{{.Os}}_{{.Arch}}"
func archiveName(version, os, arch string) string {
	return fmt.Sprintf("wipnote_%s_%s_%s.tar.gz", version, os, arch)
}

// verifySelfVersion runs `<binary> version` and checks the output contains targetVer.
func verifySelfVersion(binary, targetVer string) error {
	out, err := exec.Command(binary, "version").Output()
	if err != nil {
		return fmt.Errorf("running version command: %w", err)
	}
	if !strings.Contains(string(out), targetVer) {
		return fmt.Errorf("expected version %s in output, got: %s", targetVer, strings.TrimSpace(string(out)))
	}
	return nil
}

// verifyArchiveChecksum downloads the release's checksums file, looks up the
// expected SHA256 for archiveName, and compares it against the on-disk hash
// of tarballPath. Returns an error on mismatch. If the checksums file cannot
// be fetched or contains no entry for this archive, it prints a warning and
// returns nil — older releases predate the checksums file and we should not
// brick the upgrade path for them.
func verifyArchiveChecksum(out io.Writer, tarballPath, archiveName, checksumsURL string) error {
	expected, err := fetchExpectedChecksum(checksumsURL, archiveName)
	if err != nil {
		fmt.Fprintf(out, "warning: could not fetch checksums file (%v); skipping verification\n", err)
		return nil
	}
	if expected == "" {
		fmt.Fprintf(out, "warning: no checksum entry for %s; skipping verification\n", archiveName)
		return nil
	}
	actual, err := sha256OfFile(tarballPath)
	if err != nil {
		return fmt.Errorf("computing SHA256 of %s: %w", tarballPath, err)
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", archiveName, expected, actual)
	}
	fmt.Fprintln(out, "Checksum verified.")
	return nil
}

// fetchExpectedChecksum fetches a goreleaser-style checksums.txt and returns
// the SHA256 hex for the requested archive (matched on the last whitespace-
// separated field). Returns an empty string with no error when no matching
// entry exists.
func fetchExpectedChecksum(checksumsURL, archiveName string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(checksumsURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, checksumsURL)
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		// Each line is: "<sha256_hex>  <filename>"
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		// Use the last field for the filename to be robust against any
		// leading "*" mode marker some tools emit.
		name := fields[len(fields)-1]
		if name == archiveName {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading checksums: %w", err)
	}
	return "", nil
}

// sha256OfFile returns the hex-encoded SHA256 of the given file's contents.
func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
