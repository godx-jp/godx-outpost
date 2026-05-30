#!/usr/bin/env bash
# Build a Release archive of the Outpost iOS app and upload it to TestFlight.
#
# Prerequisites (one-time):
#   1. The app exists in App Store Connect with bundle id jp.godx.outpost.
#   2. An App Store Connect API key (Users & Access → Integrations → App Store
#      Connect API). Download the .p8 and note the Key ID + Issuer ID.
#   3. Place the key at: ~/.appstoreconnect/private_keys/AuthKey_<KEYID>.p8
#
# Run:
#   ASC_KEY_ID=XXXXXXXXXX ASC_ISSUER_ID=xxxxxxxx-xxxx-... ./scripts/release-ios.sh
#
# Bump the build number (CFBundleVersion) before each upload — TestFlight rejects
# a build number it has already seen for this version.
set -euo pipefail

: "${ASC_KEY_ID:?set ASC_KEY_ID (App Store Connect API Key ID)}"
: "${ASC_ISSUER_ID:?set ASC_ISSUER_ID (App Store Connect Issuer ID)}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IOS="$ROOT/mobile/ios"
KEY="$HOME/.appstoreconnect/private_keys/AuthKey_${ASC_KEY_ID}.p8"
SCHEME="remotehost"
ARCHIVE="$IOS/build/Outpost.xcarchive"
EXPORT="$IOS/build/export"

[ -f "$KEY" ] || { echo "API key not found: $KEY" >&2; exit 1; }

AUTH=(-allowProvisioningUpdates
  -authenticationKeyPath "$KEY"
  -authenticationKeyID "$ASC_KEY_ID"
  -authenticationKeyIssuerID "$ASC_ISSUER_ID")

cd "$IOS"

echo "==> Pods"
pod install >/dev/null

echo "==> Archive (Release)"
xcodebuild -workspace remotehost.xcworkspace -scheme "$SCHEME" \
  -configuration Release -sdk iphoneos -archivePath "$ARCHIVE" \
  "${AUTH[@]}" -quiet clean archive

echo "==> Export .ipa (app-store)"
xcodebuild -exportArchive -archivePath "$ARCHIVE" -exportPath "$EXPORT" \
  -exportOptionsPlist ExportOptions.plist "${AUTH[@]}"

IPA="$(ls "$EXPORT"/*.ipa | head -1)"
echo "==> Upload $IPA to TestFlight"
xcrun altool --upload-app --type ios --file "$IPA" \
  --apiKey "$ASC_KEY_ID" --apiIssuer "$ASC_ISSUER_ID"

echo "==> Done. The build will appear in App Store Connect → TestFlight after processing."
