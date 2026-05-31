# Deploying Outpost

Two independent artifacts: the **`outpost` daemon** (Homebrew / release binaries)
and the **iOS app** (TestFlight). Most steps below need *your* credentials
(GitHub token / Apple account) — they can't be automated away.

---

## 1. Homebrew tap + release binaries (the `outpost` CLI)

Releases are produced by GoReleaser on a `v*` git tag (`.goreleaser.yaml` +
`.github/workflows/release.yml`): it builds all platforms, publishes a GitHub
Release (archives + checksums + `.deb`/`.rpm`), and pushes a cask to the tap
**`godx-jp/homebrew-tap`** (already created).

**One-time:** the release workflow needs a `TAP_GITHUB_TOKEN` secret — a token
allowed to push to the tap repo. Create a **fine-grained PAT** (GitHub →
Settings → Developer settings → Fine-grained tokens) scoped to
`godx-jp/homebrew-tap` with **Contents: read & write**, then:

```sh
gh secret set TAP_GITHUB_TOKEN -R godx-jp/godx-outpost --body '<your-pat>'
```

**Cut a release:**

```sh
git tag v0.1.0
git push origin v0.1.0        # → CI runs GoReleaser and publishes everything
```

(Or locally, without storing a secret: `GITHUB_TOKEN=… TAP_GITHUB_TOKEN=… goreleaser release --clean`.)

**Then users install:**

```sh
brew install godx-jp/tap/outpost          # macOS
curl -fsSL https://raw.githubusercontent.com/godx-jp/godx-outpost/main/scripts/install.sh | sh   # Linux/macOS
go install github.com/godx-jp/godx-outpost/cmd/outpost@v0.1.0
```

To **update** the tap later, just tag a new version (`v0.1.1`, …) and push it;
GoReleaser refreshes the cask automatically.

---

## 2. iOS app → TestFlight

**Prerequisites (yours):**
- A **paid Apple Developer Program** membership (team `A4MCYUG555`). TestFlight
  is not available on a free personal team.
- An app record in **App Store Connect** for bundle id `jp.godx.outpost`
  (create it once at appstoreconnect.apple.com → Apps → +).

### Path A — EAS (recommended for Expo)

`eas.json` is committed. From `mobile/`:

```sh
npm i -g eas-cli            # or: npx eas-cli@latest
eas login                  # your Expo account
eas init                   # links the project (writes extra.eas.projectId to app.json)
eas build -p ios --profile production    # cloud build; sign in with your Apple ID when prompted
eas submit -p ios --latest               # uploads the build to TestFlight
```

EAS manages the distribution certificate + provisioning profile for you (it
asks for your Apple credentials interactively and stores them in your Expo
account, not in this repo). Bump `expo.version` in `app.json` for each release;
`autoIncrement` handles the build number.

### Path B — local Xcode / fastlane (no Expo cloud)

```sh
cd mobile/ios
# Archive a Release build (App Store distribution signing, auto-managed):
xcodebuild -workspace remotehost.xcworkspace -scheme remotehost \
  -configuration Release -archivePath build/outpost.xcarchive \
  -allowProvisioningUpdates archive
# Export an App Store .ipa (needs an exportOptions.plist with method=app-store):
xcodebuild -exportArchive -archivePath build/outpost.xcarchive \
  -exportPath build/ipa -exportOptionsPlist exportOptions.plist -allowProvisioningUpdates
# Upload (App Store Connect API key recommended):
xcrun altool --upload-app -t ios -f build/ipa/remotehost.ipa \
  --apiKey <KEY_ID> --apiIssuer <ISSUER_ID>
```

Then in App Store Connect → TestFlight, add testers once the build finishes
processing. (Fastlane `pilot`/`deliver` automates the upload step if preferred.)

### Before first submit — checklist
- Bump `expo.version` in `mobile/app.json` (e.g. `1.0.0`).
- App icon is set (`mobile/assets/icon.png`, 1024×1024, no alpha — present).
- Camera usage string is set (it is, in `app.json` `ios.infoPlist`).
- Export-compliance: the app uses standard TLS only → answer "No" to the
  "uses non-exempt encryption" prompt (or set `ITSAppUsesNonExemptEncryption=false`).
