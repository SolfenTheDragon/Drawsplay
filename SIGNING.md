# DrawSplay Release Signing Guide

This document explains how to set up production signing for:

1. **Windows host executable / installer** (`drawsplay_installer_<version>.exe`, installed as `DrawSplay.exe`)
2. **Android release APK** (`DrawSplay.APK`)
3. **GitHub Actions release workflow** (`.github/workflows/release.yml`)

It also covers where to obtain each key/certificate and how to store them safely.

## Virtual-display driver signing is a separate process

For beginner-friendly instructions, see
[Signing the Windows virtual-display driver](docs/windows-driver-signing.md).
That guide covers certificate/account setup, Microsoft submission, installation,
and the prerequisites for automation.

The PFX signing described below signs the host executable and can sign a driver
catalog locally; it does not obtain Microsoft's driver signature. The current
release script does not implement Microsoft submission/download and can rebuild
over a previously Microsoft-signed driver package. Do not use it unchanged to
package a returned Microsoft-signed driver. Attestation is documented by Microsoft
for testing; use HLK/WHCP for certified retail releases.

DrawSplay's IddCx driver is user-mode. Per-PC catalog signing and explicit local
certificate trust are the default installation path for unsigned catalogs, not automatically
subject to the mandatory dashboard-signing rule for new kernel-mode drivers.
The elevated installer trusts a new per-PC public certificate, immediately
deletes its private key, and preserves already trusted catalogs. Uninstall
removes the local certificates after package removal succeeds. Interactive setup
discloses this trust change; `-install -quiet` authorizes it without that dialog
(`-no-driver-install` opts out). Secure Boot/signature enforcement are unchanged.
See the walkthrough's local-signing discussion before choosing a submission route.

If the preferred driver cannot become available, setup reuses an existing Parsec
VDD or downloads its pinned, publisher-verified 0.41 installer only if missing.
Working DrawSplay installations never download/install Parsec. The fallback
does not disable Windows security and can still be blocked by machine policy;
mirroring remains the final option. `-no-driver-install` skips both installs.

---

## 1. What you need

### Windows code signing

- A **code-signing certificate** issued by a public CA (for Authenticode), exported as **PFX**.
- The **PFX password**.

### Android APK signing

- An Android **release keystore** (`.jks` or `.keystore`).
- Keystore password.
- Key alias.
- Key password.

### GitHub Actions secrets

- Base64-encoded Windows PFX.
- Base64-encoded Android keystore.
- The corresponding passwords/alias values.

---

## 2. Where to get the keys/certificates

## 2.1 Windows code-signing certificate (PFX)

Get this from a trusted certificate authority (CA), for example:

- DigiCert
- Sectigo
- GlobalSign
- SSL.com

Typical process:

1. Buy a code-signing cert (OV/EV).
2. Complete CA identity validation.
3. Install/import cert to Windows cert store (or receive a PFX/HSM workflow from CA).
4. Export to **PFX** (if your CA workflow allows export), with a strong password.

Notes:

- EV certs often use hardware-backed workflows; adapt the script/workflow if your provider requires a token/HSM signing flow instead of raw PFX.
- Keep private key material off source control and off shared drives.

## 2.2 Android release keystore

You generate this yourself (or use your org’s existing release keystore).

Example:

```powershell
keytool -genkeypair `
  -v `
  -keystore drawsplay-release.keystore `
  -alias drawsplay `
  -keyalg RSA `
  -keysize 4096 `
  -validity 3650
```

This command prompts for:

- keystore password
- key password
- certificate DN fields

Keep this keystore backed up securely. Losing it can prevent future in-place Android updates.

---

## 3. Local signing setup

The release script is:

```powershell
.\scripts\build-release.ps1
```

Set these environment variables before running it.

### 3.1 Windows signing env vars

Required for Windows signing:

- `DRAWSPLAY_WINDOWS_SIGNING_PFX_PATH`
- `DRAWSPLAY_WINDOWS_SIGNING_PFX_PASSWORD`

Optional:

- `DRAWSPLAY_WINDOWS_SIGNING_TIMESTAMP_URL`
  - default used by script: `http://timestamp.digicert.com`

Enforce fail-fast:

- `DRAWSPLAY_REQUIRE_SIGNING=1`

Example (PowerShell):

```powershell
$env:DRAWSPLAY_WINDOWS_SIGNING_PFX_PATH = "C:\secure\drawsplay-signing.pfx"
$env:DRAWSPLAY_WINDOWS_SIGNING_PFX_PASSWORD = "<pfx-password>"
$env:DRAWSPLAY_WINDOWS_SIGNING_TIMESTAMP_URL = "http://timestamp.digicert.com"
$env:DRAWSPLAY_REQUIRE_SIGNING = "1"
```

### 3.2 Android signing env vars

Required for Android release signing:

- `DRAWSPLAY_ANDROID_KEYSTORE_FILE`
- `DRAWSPLAY_ANDROID_KEYSTORE_PASSWORD`
- `DRAWSPLAY_ANDROID_KEY_ALIAS`
- `DRAWSPLAY_ANDROID_KEY_PASSWORD`

Example:

```powershell
$env:DRAWSPLAY_ANDROID_KEYSTORE_FILE = "C:\secure\drawsplay-release.keystore"
$env:DRAWSPLAY_ANDROID_KEYSTORE_PASSWORD = "<keystore-password>"
$env:DRAWSPLAY_ANDROID_KEY_ALIAS = "drawsplay"
$env:DRAWSPLAY_ANDROID_KEY_PASSWORD = "<key-password>"
```

Then build:

```powershell
.\scripts\build-release.ps1
```

---

## 4. GitHub Actions signing setup

Release workflow: `.github/workflows/release.yml`

Signing is optional by default. With no signing secrets configured, the workflow
warns and builds an unsigned Windows EXE and a debug-signed Android APK.
Debug keys on fresh CI runners are not stable: installing a later APK may require
uninstalling the previous app (which deletes its local data). Configure a permanent
Android release keystore for reliable updates.

Set the repository **Actions variable** `DRAWSPLAY_REQUIRE_SIGNING` to `1` to require
both Windows and Android production signing. Partially configured signing secrets
always fail explicitly rather than silently falling back.

To enable signing, configure these repository secrets:

### 4.1 Windows secrets

- `WINDOWS_SIGNING_PFX_BASE64`
- `WINDOWS_SIGNING_PFX_PASSWORD`
- optional `WINDOWS_SIGNING_TIMESTAMP_URL`

### 4.2 Android secrets

- `ANDROID_KEYSTORE_BASE64`
- `ANDROID_KEYSTORE_PASSWORD`
- `ANDROID_KEY_ALIAS`
- `ANDROID_KEY_PASSWORD`

## 4.3 How to create Base64 secrets

PowerShell examples:

```powershell
# Windows PFX -> one-line base64
[Convert]::ToBase64String([IO.File]::ReadAllBytes("C:\secure\drawsplay-signing.pfx")) | Set-Clipboard

# Android keystore -> one-line base64
[Convert]::ToBase64String([IO.File]::ReadAllBytes("C:\secure\drawsplay-release.keystore")) | Set-Clipboard
```

Paste each value into the matching GitHub secret.

---

## 5. Run signed releases

### Local

```powershell
.\scripts\build-release.ps1
```

### GitHub Actions

1. Push tag:
   ```bash
   git tag v0.2.0
   git push origin v0.2.0
   ```
   or run **Release** workflow manually. Manual runs select the highest stable
   `vMAJOR.MINOR.PATCH` tag and increment its patch number (for example,
   `v0.2.0` becomes `v0.2.1`). With no stable tags, the first tag is `v0.0.1`.
   Prerelease and unrelated tags are ignored.
2. Workflow will:
   - decode signing materials from secrets
   - enforce signing when `DRAWSPLAY_REQUIRE_SIGNING=1` is configured
   - build release artifacts, signing them when keys are configured
   - generate patch notes from commits since the previous reachable stable tag,
     including a full-changelog link and download instructions
   - publish release assets and create the selected tag at the built commit

Publication runs are serialized to prevent concurrent manual runs from selecting
the same next tag. The new tag is created only after both builds succeed.

---

## 6. Security requirements (important)

- Never commit:
  - `.pfx` files
  - Android keystores
  - passwords
  - base64 secret blobs
- Rotate credentials immediately if exposed.
- Restrict repo secret access to maintainers.
- Prefer short-lived local environment variables over persistent user/system variables on shared machines.
- Store source key material in a secure vault (enterprise secret manager/HSM-backed storage where available).

---

## 7. Troubleshooting

- **`signtool.exe not found`**:
  - Install Windows SDK Signing Tools.
  - Ensure `signtool` is on PATH or available under `Windows Kits\10\bin`.

- **Android release still debug-signed locally**:
  - Confirm all four `DRAWSPLAY_ANDROID_*` variables are set in the same shell session.

- **CI fails saying signing settings missing**:
  - Verify all required secrets are present and named exactly as listed.

- **Timestamp/signature errors**:
  - Try another RFC3161 timestamp URL from your CA.
