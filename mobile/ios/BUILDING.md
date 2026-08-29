# Building the Sense iOS app

*Part of [Reviving the Hello Sense Sleep System](../../README.md).*

Open `Sense.xcworkspace`, not the `.xcodeproj`. Do **not** run `pod install`:
`Pods/` and `Vendor/` are committed because the pod specs are from 2016 and
cannot be reliably resolved any more, and `Vendor/SenseKit` carries local BLE
fixes that are not in any published pod.

## What you need to change

| Build setting | In | What it is |
|---|---|---|
| `SENSE_API_URL` | `project.pbxproj` | where orb is. A LAN address for Debug/Dev, which build with ATS disabled |
| `DEVELOPMENT_TEAM` | `project.pbxproj` | your Apple Team ID |
| `SENSE_CLIENT_ID` | `project.pbxproj` | must match a row in orb's `oauth_applications` |

`Sense-Info.plist` holds no values: every one of them is a `${BUILD_SETTING}`
reference resolved at build time.

`Signing.xcconfig.example` shows how to move these into a gitignored local file
if you would rather not edit `project.pbxproj`. It is not wired in by default.

## What was removed before this was published

`SENSE_ANALYTICS_TOKEN` (Hello's Segment write key) and
`SENSE_CRASH_REPORT_TOKEN` (their Bugsnag API key) were real 32-character
credentials belonging to Hello, present in 13 build configurations. They are
blanked.

Nothing breaks: both call sites guard on `[token length] > 0`, so an empty value
means analytics and crash reporting are simply off. The Debug configuration
already shipped an empty crash token for exactly that reason.

## What is deliberately still here

- **`DEVELOPMENT_TEAM`**, a 10-character Apple Team ID. An identifier, not a
  credential. Blanking it would only mean re-selecting a team in Xcode.
- **`SENSE_CLIENT_ID`**. The app is a public OAuth client and sends no client
  secret, so this is not confidential by design; anyone with a build of any
  iOS app can read its client id. What protects the token endpoint is the
  account password and rate limiting, which is worth checking before that
  endpoint faces the internet.
- **A private LAN address** in `SENSE_API_URL`. RFC1918, meaningless off the
  network.

No code-signing material is in this tree, and none ever should be: signing
identities live in the macOS Keychain and provisioning profiles in
`~/Library/MobileDevice/Provisioning Profiles/`. `.gitignore` carries patterns
for all of them so a stray export cannot be committed.

## Divergence from upstream

See [`UPSTREAM/`](UPSTREAM/). 17 files differ from `hello/suripu-ios` at
`e7772dc`, captured as a patch.
