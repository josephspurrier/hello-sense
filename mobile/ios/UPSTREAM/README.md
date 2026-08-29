# Divergence from Hello's suripu-ios

This app is Hello's iOS client, modified. It was maintained as a clone with
uncommitted working-tree edits until 2026-08-29, when it moved here and the
nested `.git` was dropped. This directory is what preserves the provenance that
`.git` used to carry.

## Base

    https://github.com/hello/suripu-ios
    e7772dc347243f1aba19a0c49c0269e866844514
    "Theme the whole hierarchy on a cold launch, not just Themed controllers"

`divergence.patch` is the complete diff from that commit to the tree as moved:
**17 files, 338 insertions, 55 deletions.** To see what was changed rather than
what the app now is, read that patch. To diff against upstream again, clone
`hello/suripu-ios` at the commit above.

Hello's repositories carry no licence. See the repository NOTICE.

## What was changed, and roughly why

| Area | Files |
|---|---|
| BLE and pairing | `Vendor/SenseKit/.../SENSense.{h,m}`, `SENSenseManager.m`, `SENServiceDevice.m`, `Vendor/LGBluetooth/.../LGCentralManager.m` |
| Server selection | `SleepModel/HEMSelectHostPresenter.m`, `HEMDeviceService.m`, `HEMDevicesPresenter.m`, `HEMSettingsPresenter.m` |
| Timeline and insights UI | `HEMSleepGraphViewController.m`, `HEMTimelineFeedbackViewController.m`, `HEMInsightCollectionViewCell.m` |
| Calendar transition fix | `HEMZoomAnimationTransitionDelegate.m` |
| Picker control | `Vendor/NAPickerView/...` and its `Pods/` copy, changed identically |
| Project settings | `Sense.xcodeproj/project.pbxproj`, `SleepModel/Settings.storyboard` |

`NAPickerView.m` exists twice, under `Vendor/` and under `Pods/`, and both were
edited the same way. Change one without the other and which version builds
depends on target membership.

## Why Pods/ and Vendor/ are committed

Upstream tracks both (2,472 and 308 files). The pod specs are from 2016 and
`pod install` cannot be relied on to reproduce them, so the vendored copies are
the only reliable build input. `Vendor/SenseKit` in particular carries local BLE
fixes and is not a stock pod at all.
