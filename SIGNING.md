# DrawSplay Release Signing Guide

This document explains how to set up production signing for:

1. **Windows host executable / installer** (`drawsplay_installer_<version>.exe`, installed as `DrawSplay.exe`)
2. **Android release APK** (`DrawSplay.APK`)
3. **GitHub Actions release workflow** (`.github/workflows/release.yml`)
4. **Update checksum manifests** (`SHA256SUMS.txt` + `SHA256SUMS.sig`)

It also covers where to obtain each key/certificate and how to store them safely.

## Virtual-display driver signing is a separate process

The public [build guide](BUILD.md#bundled-virtual-display-driver) covers local
driver compilation and per-PC installation. For Microsoft submission, follow
[attestation signing](https://learn.microsoft.com/windows-hardware/drivers/dashboard/code-signing-attestation)
or the [Windows Hardware Lab Kit](https://learn.microsoft.com/windows-hardware/test/hlk/getstarted/windows-hardware-lab-kit)
documentation. Private maintainer notes are not required for the local path.

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
Choose a submission route appropriate to your distribution and target policy.

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
- A raw PEM Ed25519 private key matching the clients' pinned update public key.

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

The app's in-app updater installs a release only when it is signed with the
same key as the installed app. Releases built without these secrets are
development builds signed with a temporary debug key, which changes between runners,
so their in-app updates fail and users must reinstall by hand. Configure the
keystore secrets before relying on in-app updates.

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

This builds Windows/Android archives and local unsigned checksums. It does not
produce the final multi-platform update manifest or its detached signature;
the Release workflow does that after assembling all platform bundles.

---

## 4. GitHub Actions signing setup

Release workflow: `.github/workflows/release.yml`

The Release workflow always sets `DRAWSPLAY_REQUIRE_SIGNING=1`. Both Windows
and Android production signing are mandatory; a repository Actions variable
does not override this. Missing or partial signing configuration fails the
release. The ordinary CI workflow and local development builds can still use
development signing.

Configure these repository secrets before publishing:

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

### 4.4 Signed update checksums

Set `DRAWSPLAY_RELEASE_SIGNING_KEY` to the **raw, unencrypted Ed25519 private-key
PEM**, including the `BEGIN PRIVATE KEY` / `END PRIVATE KEY` lines and newlines.
This is not the Windows PFX, Android keystore or a Base64-encoded PEM.

The workflow signs the exact bytes of the final `SHA256SUMS.txt` using:

```bash
openssl pkeyutl -sign -inkey release-signing-key.pem -rawin \
  -in SHA256SUMS.txt -out SHA256SUMS.sig
```

`SHA256SUMS.sig` must be a raw 64-byte signature, not Base64 or hex. Publish both
files with the assets they describe. The host and tablet verify the signature
against their pinned public key before trusting the checksums.

For the existing distribution, recover the matching key from secure storage;
do not generate a replacement. Its public key is pinned in `host/update.go`
and the Android release verifier:

```text
247960eda0e2c352ac115ac991abcc45fccca85fb429bc0e5a54426ed4aec8f1
```

To check an existing private key without printing it:

```bash
openssl pkey -in release-signing-key.pem -pubout -outform DER \
  -out release-signing-public.der
python3 -c "from pathlib import Path; print(Path('release-signing-public.der').read_bytes()[-32:].hex())"
```

For a **new distribution only**, generate a key in secure storage:

```bash
umask 077
openssl genpkey -algorithm ED25519 -out release-signing-key.pem
```

Pin its derived public key in both clients before distributing them, and store
the private PEM only in the secret/vault. Changing the secret alone breaks
updates for installed clients. Rotation requires a planned trust transition
while the old key is available, or a separately verified manual installation;
existing single-key clients cannot accept an arbitrary replacement key.

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
   - require production Windows and Android signing
   - build and sign release artifacts
   - generate final checksums and their Ed25519 detached signature
   - generate patch notes from commits since the previous reachable stable tag,
     including a full-changelog link and download instructions
   - publish release assets and create the selected tag at the built commit

The entire workflow, from tag selection through builds and publication, is
serialized to prevent concurrent manual runs from selecting the same next tag.
The new tag is created only after all platform builds succeed.

---

## 6. Security requirements (important)

- Never commit:
  - `.pfx` files
  - Android keystores
  - passwords
  - base64 secret blobs
  - Ed25519 private-key PEM files
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
