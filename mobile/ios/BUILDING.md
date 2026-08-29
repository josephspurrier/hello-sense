# Building the Sense iOS app

*Part of [Reviving the Hello Sense Sleep System](../../README.md).*

Open `Sense.xcworkspace`, not the `.xcodeproj`. Do **not** run `pod install`:
`Pods/` and `Vendor/` are committed because the pod specs are from 2016 and
cannot be reliably resolved any more, and `Vendor/SenseKit` carries local BLE
fixes that are not in any published pod.

## What you need to change

Copy the template and edit it. Nothing in `project.pbxproj` needs touching:

```bash
cp Config/Local.xcconfig.example Config/Local.xcconfig
```

| Setting | Where | What it is |
|---|---|---|
| `SENSE_API_URL` | `Config/Local.xcconfig` | where orb is, for example `https://sense.example.com:8443` |
| `SENSE_APP_ID_PROD` | `Config/Local.xcconfig` | bundle id for Release and Beta |
| `SENSE_APP_ID_TEST` | `Config/Local.xcconfig` | bundle id for Debug and Dev |
| `DEVELOPMENT_TEAM` | `project.pbxproj` | your Apple Team ID |
| `SENSE_CLIENT_ID` | `project.pbxproj` | must match a row in orb's `oauth_applications` |

`Config/Local.xcconfig` is gitignored. `Config/Base.xcconfig` holds placeholders
and includes it last, so a fresh clone builds without any setup and whatever you
put in the local file wins.

Two things about that file that look odd and are not:

- **`SLASH = /`, with URLs written `https:$(SLASH)$(SLASH)host`.** In an xcconfig
  `//` starts a comment, with no escape, so a literal URL is silently truncated
  to `https:`.
- **Two bundle id variables rather than one.** The keychain group, both app
  groups and the widget's bundle id are all derived from whichever one applies to
  the configuration being built, so setting these two sets six values. The
  entitlements file needs both names to exist; see below.

`Sense-Info.plist` holds no values: every one of them is a `${BUILD_SETTING}`
reference resolved at build time.

## Identifiers still in the repository

`Extensions/RoomConditions/Sense.entitlements` still spells out both bundle ids
six times, and is the only place that does. It is annotated in the file itself.

It was left literal on purpose. The file lists **both** identifier families so
that one entitlements file serves every configuration, which a single
`$(SENSE_APP_ID)` cannot reproduce; that is why `SENSE_APP_ID_PROD` and
`SENSE_APP_ID_TEST` exist as separate variables, ready for it. Xcode does expand
build settings in entitlements, as the `$(AppIdentifierPrefix)` already in the
file shows, so the change itself is mechanical.

The care is in the verification, because entitlements feed code signing and a
wrong value **does not fail the build**. The app installs, then cannot reach the
keychain or the shared container, which presents as being mysteriously logged out
or as an empty widget. So check the signed product, not the source:

```bash
codesign -d --entitlements :- <path>/Sense.app
codesign -d --entitlements :- <path>/Sense.app/PlugIns/RoomConditionsExtension.appex
```

and diff both against what they produce today. The app groups and keychain groups
must come out byte-identical.

Also confirm the identifiers and their four `group.` app groups exist in your
Apple Developer account. Parameterising does not change the expanded values, so
nothing there should need creating, but it is cheaper to check first.

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
