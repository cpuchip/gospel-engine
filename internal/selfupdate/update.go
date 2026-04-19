// Package selfupdate implements safe in-place updates for the gospel-mcp
// client. The server bakes built binaries into its container image at
// /opt/mcp-binaries and serves them at /download/gospel-mcp-{os}-{arch}.
// On startup the client checks /api/version, compares against its own
// build-time version, and replaces itself if different.
//
// Safeguards:
//   - SHA-256 verified before swap (when server provides a hash)
//   - Previous binary kept as `<self>.prev` for one-command rollback
//   - GOSPEL_AUTO_UPDATE=false disables updates entirely
//   - GOSPEL_AUTO_UPDATE_GATE=first-run requires the user to have used the
//     binary at least once (sentinel `<self>.first-run-ok`) before any
//     update is attempted. Default is gate=on.
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type versionResp struct {
	Version  string   `json:"version"`
	API      string   `json:"api"`
	Binaries []string `json:"binaries"`
	// Optional, server may add per-platform sha256 in future.
	SHA map[string]string `json:"sha256,omitempty"`
}

// Check is a best-effort self-update. Errors are returned for logging but
// never cause the caller to abort — the user can always run the existing
// binary.
func Check(serverURL, currentVersion string) error {
	if os.Getenv("GOSPEL_AUTO_UPDATE") == "false" {
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	sentinel := exe + ".first-run-ok"

	// Drop the sentinel so the NEXT run is allowed to update.
	defer touchSentinel(sentinel)

	// First-run gate: no auto-update until the user has run the binary
	// successfully at least once.
	if os.Getenv("GOSPEL_AUTO_UPDATE_GATE") != "off" {
		if _, err := os.Stat(sentinel); err != nil {
			return nil
		}
	}

	cli := &http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Get(strings.TrimRight(serverURL, "/") + "/api/version")
	if err != nil {
		return fmt.Errorf("version check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("version endpoint returned %d", resp.StatusCode)
	}
	var v versionResp
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return fmt.Errorf("decode version: %w", err)
	}
	if v.Version == "" || v.Version == currentVersion {
		return nil
	}

	binName := fmt.Sprintf("gospel-mcp-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	dlURL := strings.TrimRight(serverURL, "/") + "/download/" + binName

	// Stage download next to the running binary.
	stage := exe + ".new"
	if err := download(cli, dlURL, stage); err != nil {
		return err
	}

	// Verify SHA-256 if the server provided one.
	if want := v.SHA[binName]; want != "" {
		got, err := sha256File(stage)
		if err != nil {
			os.Remove(stage)
			return fmt.Errorf("hashing staged binary: %w", err)
		}
		if !strings.EqualFold(got, want) {
			os.Remove(stage)
			return fmt.Errorf("sha256 mismatch (want=%s got=%s)", want, got)
		}
	}

	if err := os.Chmod(stage, 0o755); err != nil {
		// Non-fatal on Windows.
		_ = err
	}

	return swapAndReexec(exe, stage, v.Version)
}

func download(cli *http.Client, urlStr, dest string) error {
	resp, err := cli.Get(urlStr)
	if err != nil {
		return fmt.Errorf("downloading: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	tmp, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer tmp.Close()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		return err
	}
	return nil
}

func sha256File(path string) (string, error) {
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

func touchSentinel(path string) {
	if f, err := os.Create(path); err == nil {
		f.Close()
	}
}

// swapAndReexec is implemented per-OS in update_unix.go and update_windows.go.
