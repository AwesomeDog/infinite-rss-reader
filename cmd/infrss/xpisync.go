package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// syncXPI silently checks if the installed .xpi matches the embedded version.
// If the versions differ, it rebuilds and overwrites the .xpi and clears the startup cache.
func syncXPI() {
	profiles, err := findThunderbirdProfiles()
	if err != nil {
		log.Printf("XPI sync: cannot find profiles: %v", err)
		return
	}

	embeddedVersion := addonVersion()

	for _, profile := range profiles {
		xpiPath := filepath.Join(profile, "extensions", extensionID+".xpi")

		installedVersion := getInstalledXPIVersion(xpiPath)

		if installedVersion == embeddedVersion {
			log.Printf("XPI sync: %s is up to date (v%s)", filepath.Base(profile), embeddedVersion)
			continue
		}

		log.Printf("XPI sync: updating %s from v%s to v%s", filepath.Base(profile), installedVersion, embeddedVersion)

		xpiData, err := buildXPI()
		if err != nil {
			log.Printf("XPI sync: failed to build XPI: %v", err)
			continue
		}

		if err := installXPI(profile, xpiData); err != nil {
			log.Printf("XPI sync: failed to install XPI to %s: %v", profile, err)
			continue
		}

		// Clear startup cache
		os.RemoveAll(filepath.Join(profile, "startupCache"))
		log.Printf("XPI sync: updated %s successfully", filepath.Base(profile))
	}
}

// getInstalledXPIVersion reads the version from an installed .xpi file.
func getInstalledXPIVersion(xpiPath string) string {
	data, err := os.ReadFile(xpiPath)
	if err != nil {
		return ""
	}

	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ""
	}

	for _, f := range r.File {
		if f.Name == "manifest.json" {
			rc, err := f.Open()
			if err != nil {
				return ""
			}
			defer rc.Close()

			var manifest map[string]interface{}
			if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
				return ""
			}

			if v, ok := manifest["version"].(string); ok {
				return v
			}
			return ""
		}
	}

	return ""
}
