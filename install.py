#!/usr/bin/env python3
import os, sys, json, shutil, zipfile
from pathlib import Path

# Configuration
PROJECT_DIR = Path(__file__).parent.absolute()
APP_DIR = PROJECT_DIR / "app"
ADDON_DIR = PROJECT_DIR / "add-on"


def get_tb_paths():
    """Returns (native_messaging_dir, profiles_dir) - all user-level paths."""
    if sys.platform == 'win32':
        return None, Path.home() / "AppData/Roaming/Thunderbird/Profiles"
    elif sys.platform == 'darwin':
        # macOS: Thunderbird uses its own NativeMessagingHosts directory
        home = Path.home()
        return home / "Library/Application Support/Thunderbird/NativeMessagingHosts", home / "Library/Thunderbird/Profiles"

    # Linux: Snap installation (Ubuntu default)
    home = Path.home()
    snap_profiles = home / "snap/thunderbird/common/.thunderbird"

    if not snap_profiles.exists():
        print(f"\n❌ ERROR: Snap Thunderbird not found!")
        print(f"   Expected: {snap_profiles}")
        print(f"\n💡 This script only supports Ubuntu Snap Thunderbird installation")
        print(f"   Install Snap version: sudo snap install thunderbird")
        print(f"   Or manually install by following this script")
        sys.exit(1)

    return home / ".mozilla/native-messaging-hosts", snap_profiles


def configure_manifest():
    """Configures rss_bridge.json and installs it."""
    print("\n📝 Configuring Native Messaging...")
    manifest_path = APP_DIR / "rss_bridge.json"

    with open(manifest_path, 'r') as f:
        config = json.load(f)

    # Set executable path
    if sys.platform == 'win32':
        bat_path = APP_DIR / "rss_bridge_win.bat"
        with open(bat_path, 'w') as f:
            f.write(f'@echo off\ncall python "{APP_DIR / "rss_bridge.py"}"\n')
        config['path'] = str(bat_path)
    else:
        script_path = APP_DIR / "rss_bridge.py"
        config['path'] = str(script_path)
        os.chmod(script_path, 0o755)  # Ensure executable

    # Save updated manifest
    with open(manifest_path, 'w') as f:
        json.dump(config, f, indent=2)

    # Install manifest
    native_hosts_dir, _ = get_tb_paths()
    if sys.platform == 'win32':
        try:
            import winreg
            key = winreg.CreateKey(winreg.HKEY_CURRENT_USER, r'Software\Mozilla\NativeMessagingHosts\rss_bridge')
            winreg.SetValueEx(key, '', 0, winreg.REG_SZ, str(manifest_path))
            winreg.CloseKey(key)
            print(f"   ✓ Registry updated: {manifest_path}")
        except Exception as e:
            print(f"   ❌ Registry Error: {e}")
            raise
    else:
        try:
            native_hosts_dir.mkdir(parents=True, exist_ok=True)
            shutil.copy(manifest_path, native_hosts_dir / "rss_bridge.json")
            print(f"   ✓ Installed to: {native_hosts_dir}")
        except Exception as e:
            print(f"   ❌ Failed to install manifest to {native_hosts_dir}: {e}")
            raise


def install_extension():
    """Packages and installs the XPI."""
    print("\n📦 Installing Extension...")
    xpi_path = PROJECT_DIR / "out/rss_bridge.xpi"
    xpi_path.parent.mkdir(exist_ok=True)

    # Create XPI
    with zipfile.ZipFile(xpi_path, 'w', zipfile.ZIP_DEFLATED) as z:
        for root, _, files in os.walk(ADDON_DIR):
            for file in files:
                p = Path(root) / file
                z.write(p, p.relative_to(ADDON_DIR))

    _, profiles_dir = get_tb_paths()
    profiles = list(profiles_dir.glob("*.default*"))

    if profiles:
        profile = profiles[0]  # Pick the first default profile
        dest = profile / "extensions/rss_bridge@example.org.xpi"
        dest.parent.mkdir(exist_ok=True)
        shutil.copy(xpi_path, dest)
        print(f"   ✓ Installed to: {dest}")

        # Clear startup cache to force reload
        shutil.rmtree(profile / "startupCache", ignore_errors=True)
    else:
        print(f"   ⚠️ No profile found. Manually install: {xpi_path}")


def main():
    print(f"🚀 Installing RSS Bridge on {sys.platform}...")
    print(f"⚠️ Please make sure Thunderbird process is not running...")
    try:
        configure_manifest()
        install_extension()

        print("\n✅ Done! Next Steps:")
        print("   1. Restart Thunderbird")
        print("   2. Accept the external extension if prompted")
        print("   3. Ensure xpinstall.signatures.required = false in Config Editor")
        print("   4. HTTP API will run on port 7654")
    except Exception as e:
        print(f"\n❌ Error: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()
