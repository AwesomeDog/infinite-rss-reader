package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	extensionID  = "infrss@awesomedog.github.io"
	manifestName = "infrss.json"
)

// runInstallMode performs the self-install workflow when the user double-clicks the binary.
func runInstallMode() {
	fmt.Println("🚀 Infinite RSS Reader — Install Mode")
	fmt.Println()

	// 1. Determine install path and copy self
	installPath, err := installBinary()
	if err != nil {
		fmt.Printf("❌ Failed to install binary: %v\n", err)
		waitForEnter()
		os.Exit(1)
	}

	// 2. Find Thunderbird profiles
	profiles, err := findThunderbirdProfiles()
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		waitForEnter()
		os.Exit(1)
	}

	// 3. Build XPI from embedded add-on files
	xpiData, err := buildXPI()
	if err != nil {
		fmt.Printf("❌ Failed to build extension: %v\n", err)
		waitForEnter()
		os.Exit(1)
	}

	// 4. Install XPI to each profile
	for _, profile := range profiles {
		if err := installXPI(profile, xpiData); err != nil {
			fmt.Printf("⚠️  Failed to install extension to %s: %v\n", profile, err)
		} else {
			fmt.Printf("   ✓ Extension installed to: %s\n", profile)
		}

		// 5. Clear startup cache
		cacheDir := filepath.Join(profile, "startupCache")
		os.RemoveAll(cacheDir)
	}

	// 6. Write native messaging manifest
	if err := installManifest(installPath); err != nil {
		fmt.Printf("❌ Failed to install manifest: %v\n", err)
		waitForEnter()
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("✅ Installation complete!")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("   1. Open Thunderbird")
	fmt.Println("   2. Go to Settings → General → Config Editor")
	fmt.Println("   3. Set xpinstall.signatures.required = false")
	fmt.Println("   4. Restart Thunderbird")
	fmt.Println("   5. Open http://localhost:7654 in your browser")
	fmt.Println()
	waitForEnter()
}

// installBinary copies the running binary to ~/.local/bin/infrss.
func installBinary() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	binName := "infrss"
	if runtime.GOOS == "windows" {
		binName = "infrss.exe"
	}
	installDir := filepath.Join(home, ".local", "bin")
	installPath := filepath.Join(installDir, binName)

	// Get our own path
	selfPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot determine own path: %w", err)
	}
	selfPath, err = filepath.EvalSymlinks(selfPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve symlinks: %w", err)
	}

	// Skip if already running from install path
	if filepath.Clean(selfPath) == filepath.Clean(installPath) {
		fmt.Println("   ✓ Already running from install path")
		return installPath, nil
	}

	// Check if target exists and compare versions
	if _, err := os.Stat(installPath); err == nil {
		out, err := exec.Command(installPath, "--version").Output()
		if err == nil {
			installedVersion := strings.TrimSpace(string(out))
			ownVersion := fmt.Sprintf("infrss %s", version)
			if installedVersion == ownVersion {
				fmt.Printf("   ✓ Same version already installed (%s)\n", version)
				return installPath, nil
			}
			fmt.Printf("   Updating: %s → %s\n", installedVersion, ownVersion)
		}
	}

	// Create directory and copy
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", installDir, err)
	}

	selfData, err := os.ReadFile(selfPath)
	if err != nil {
		return "", fmt.Errorf("cannot read own binary: %w", err)
	}

	// Write to a temp file first, then rename (atomic).
	// This avoids overwriting the existing binary in place, which
	// on macOS Apple Silicon invalidates the code signature cache
	// (AMFI) and causes "Killed: 9" on the next launch.
	tmpPath := installPath + ".tmp"
	if err := os.WriteFile(tmpPath, selfData, 0755); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("cannot write to %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, installPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("cannot rename %s → %s: %w", tmpPath, installPath, err)
	}

	fmt.Printf("   ✓ Installed binary to: %s\n", installPath)
	return installPath, nil
}

// findThunderbirdProfiles returns paths to Thunderbird default profiles.
func findThunderbirdProfiles() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	var profilesDir string
	switch runtime.GOOS {
	case "darwin":
		profilesDir = filepath.Join(home, "Library", "Thunderbird", "Profiles")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		profilesDir = filepath.Join(appData, "Thunderbird", "Profiles")
	case "linux":
		// Try Snap first, then standard
		snapDir := filepath.Join(home, "snap", "thunderbird", "common", ".thunderbird")
		if info, err := os.Stat(snapDir); err == nil && info.IsDir() {
			profilesDir = snapDir
		} else {
			profilesDir = filepath.Join(home, ".thunderbird")
		}
	default:
		return nil, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	matches, err := filepath.Glob(filepath.Join(profilesDir, "*.default*"))
	if err != nil {
		return nil, fmt.Errorf("error scanning profiles: %w", err)
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no Thunderbird profile found in %s", profilesDir)
	}

	return matches, nil
}

// buildXPI creates an in-memory ZIP (XPI) from the embedded add-on files,
// patching manifest.json's version to match the binary version.
func buildXPI() ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	err := fs.WalkDir(addonFS, "add-on", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := addonFS.ReadFile(path)
		if err != nil {
			return err
		}

		// Strip the "add-on/" prefix for the zip entry
		entryName := strings.TrimPrefix(path, "add-on/")

		// Patch manifest.json version
		if entryName == "manifest.json" {
			data, err = patchManifestVersion(data)
			if err != nil {
				return fmt.Errorf("patching manifest version: %w", err)
			}
		}

		f, err := w.Create(entryName)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}

	if err := w.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// patchManifestVersion replaces the version field in manifest.json with the binary version.
func patchManifestVersion(data []byte) ([]byte, error) {
	var manifest map[string]interface{}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	manifest["version"] = addonVersion()

	return json.MarshalIndent(manifest, "", "  ")
}

// addonVersion returns a Thunderbird-compatible version string
// (up to 4 dot-separated integers, no leading zeros).
func addonVersion() string {
	ver := version
	ver = strings.TrimPrefix(ver, "v")

	// Strip git describe suffix: "1.2.3-4-gabcdef" → "1.2.3"
	if idx := strings.Index(ver, "-"); idx != -1 {
		ver = ver[:idx]
	}

	// Validate: must be dot-separated integers (e.g. "1.0.0")
	if isValidAddonVersion(ver) {
		return ver
	}

	return "0.0.1"
}

// isValidAddonVersion checks if a string is a valid Thunderbird extension version:
// 1-4 dot-separated integers, no leading zeros, each ≤ 9 digits.
func isValidAddonVersion(v string) bool {
	if v == "" {
		return false
	}
	parts := strings.Split(v, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 9 {
			return false
		}
		if len(p) > 1 && p[0] == '0' {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// installXPI writes the XPI data to the profile's extensions directory.
func installXPI(profilePath string, xpiData []byte) error {
	extDir := filepath.Join(profilePath, "extensions")
	if err := os.MkdirAll(extDir, 0755); err != nil {
		return err
	}

	xpiPath := filepath.Join(extDir, extensionID+".xpi")
	return os.WriteFile(xpiPath, xpiData, 0644)
}

// installManifest writes the native messaging manifest and registers it.
func installManifest(binaryPath string) error {
	// Build manifest from template
	var manifest map[string]interface{}
	if err := json.Unmarshal(manifestTemplate, &manifest); err != nil {
		return fmt.Errorf("invalid manifest template: %w", err)
	}
	manifest["path"] = binaryPath

	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	switch runtime.GOOS {
	case "darwin":
		dir := filepath.Join(home, "Library", "Mozilla", "NativeMessagingHosts")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		dest := filepath.Join(dir, manifestName)
		if err := os.WriteFile(dest, manifestData, 0644); err != nil {
			return err
		}
		fmt.Printf("   ✓ Manifest installed to: %s\n", dest)

	case "linux":
		dir := filepath.Join(home, ".mozilla", "native-messaging-hosts")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		dest := filepath.Join(dir, manifestName)
		if err := os.WriteFile(dest, manifestData, 0644); err != nil {
			return err
		}
		fmt.Printf("   ✓ Manifest installed to: %s\n", dest)

	case "windows":
		// Write manifest file next to binary
		dir := filepath.Dir(binaryPath)
		dest := filepath.Join(dir, manifestName)
		if err := os.WriteFile(dest, manifestData, 0644); err != nil {
			return err
		}

		// Register in Windows Registry
		regPath := `HKCU\Software\Mozilla\NativeMessagingHosts\infrss`
		cmd := exec.Command("reg", "add", regPath, "/ve", "/t", "REG_SZ", "/d", dest, "/f")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("registry write failed: %s: %w", string(out), err)
		}
		fmt.Printf("   ✓ Manifest installed + registry updated: %s\n", dest)

	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}

	return nil
}

// waitForEnter waits for the user to press Enter.
func waitForEnter() {
	fmt.Print("Press Enter to exit...")
	buf := make([]byte, 1)
	os.Stdin.Read(buf)
}
