#!/bin/bash
# Builds the vx menu bar app (~/Applications/vx.app).
# The vx Go binary is embedded in the app bundle so the app is self-contained.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="$ROOT/dist/vx.app"
BIN="$ROOT/vxbar/build/vxbar"

echo "==> building vx (Go)"
mkdir -p "$ROOT/bin" "$ROOT/vxbar/build" "$ROOT/dist"
(cd "$ROOT" && go build -o bin/vx ./cmd/vx)

echo "==> compiling vxbar (Swift)"
swiftc -O -swift-version 5 \
  -framework AppKit -framework ServiceManagement \
  "$ROOT/vxbar/main.swift" -o "$BIN"

echo "==> assembling $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/vxbar"
cp "$ROOT/bin/vx" "$APP/Contents/Resources/vx"
chmod +x "$APP/Contents/MacOS/vxbar" "$APP/Contents/Resources/vx"

cat > "$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleExecutable</key>
	<string>vxbar</string>
	<key>CFBundleIdentifier</key>
	<string>com.vx.menubar</string>
	<key>CFBundleName</key>
	<string>vx</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>0.1.0</string>
	<key>CFBundleVersion</key>
	<string>1</string>
	<key>LSMinimumSystemVersion</key>
	<string>13.0</string>
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
PLIST

echo "==> ad-hoc signing"
codesign --force -s - "$APP" 2>/dev/null || true

echo "==> installing to ~/Applications/vx.app"
mkdir -p "$HOME/Applications"
rm -rf "$HOME/Applications/vx.app"
cp -R "$APP" "$HOME/Applications/vx.app"

echo "done."
echo "  Launch:   open ~/Applications/vx.app"
echo "  Start at login: menu bar icon → 'Start at Login'"
