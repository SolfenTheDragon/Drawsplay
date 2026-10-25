# Signing the Windows virtual-display driver: a beginner's guide

This guide explains how to get DrawSplay's virtual-display driver signed,
install it, and eventually automate the process.

Instructions and Microsoft requirements checked on October 24, 2026.
Microsoft's portal screens and policies can change; the official links at the
end are the source of truth.

## Start here

**You do not need to buy a certificate just to test wireless streaming in
mirror mode.** You need a working virtual-display driver for **extend mode**:
making the tablet a separate screen instead of copying your existing screen.

The current unsigned package was rejected by Windows with:

```text
0x800B0100: No signature was present in the subject.
Driver package catalog file does not contain a signature,
and Code Integrity is enforced.
```

An older installer that does not sign the catalog locally cannot fix that signature
just by being run as administrator again. Updated installers perform local signing.
Signing only `DrawSplay.exe` does not fix it either.

### Can the installer sign it locally instead?

Yes: updated installers implement local catalog signing for DrawSplay's
**user-mode (UMDF)** IddCx driver.
Microsoft's mandatory dashboard-signing policy for new **kernel-mode** drivers
must not be applied indiscriminately to it. A locally signed catalog with an
explicitly trusted per-PC certificate is a separate option worth validating;
it is not necessarily the same as enabling Windows test-signing mode.

The elevated installer creates a per-PC code-signing certificate, signs a
catalog covering the exact INF and DLL, adds the public certificate to
LocalMachine Root and TrustedPublisher, deletes the private key immediately,
and installs the package. Uninstall removes the package and the trust it created;
failed package removal retains trust with a warning so it can be retried.
This changes machine-wide certificate trust and needs informed consent.
Managed App Control or other policies may still reject the driver.

If this fails, setup first looks for an existing Parsec VDD. Only when the
DrawSplay driver is unavailable and Parsec is missing does it download and
install the pinned, signature-verified Parsec 0.41 fallback (unless automatic
tool installation is disabled). A working DrawSplay driver means **no Parsec
download or installation**. If both drivers fail, the tablet mirrors instead.
Parsec fallback resolutions/refresh rates are limited to its existing modes.

DrawSplay uses a shared signing helper for its WinUSB installation package and
the IddCx catalog. An already trusted IddCx catalog is preserved; a signed but
invalid/untrusted catalog is rejected rather than overwritten. Driver files are
checked against the embedded package before signing from an administrator-writable
staging directory. Installation, driver startup, monitor creation, restart/update,
and uninstall still need verification on each supported Windows configuration.
Do not promise that disabling Secure Boot is necessary or that locally
trusting a certificate overrides kernel-mode signing policy.

The rest of this guide describes the **Microsoft-submission route**, not a
requirement that every user-mode deployment must use Microsoft submission.

There are several different routes:

| Your goal | Route |
| --- | --- |
| Test wireless immediately without changing Windows security | Keep using mirror mode. |
| Avoid Microsoft submission for this user-mode driver | Use the updated installer's per-PC signing/trust path described above. |
| Get a Microsoft-signed driver for controlled Windows 10/11 desktop testing | Follow the attestation-signing walkthrough below. |
| Publish a properly certified driver for customers and retail Windows Update | Complete Windows Hardware Lab Kit (HLK) testing and submit through the Windows Hardware Compatibility Program (WHCP). |

Microsoft currently describes attestation signing as **for testing purposes
only**. It is not Windows certification and is not a substitute for the retail
certification process. A signature also does not guarantee that the driver
works correctly.

### Use the local-signing installer, step by step

1. Build or obtain an updated Windows host containing local-signing support.
2. Run it with `-install`, or use `scripts\install-host-windows.ps1 -SourceExe`
   with the new executable's full path.
3. In interactive setup, accept the virtual-display installation and certificate
   trust disclosure. Choose No to skip that driver and use mirror mode.
4. Approve the Windows administrator prompt.
5. Setup signs an unsigned catalog locally, trusts the public certificate,
   deletes its private key, and installs the driver. It does not turn off
   Secure Boot, enable test mode, or disable signature enforcement.
6. Connect the tablet and check Windows Display Settings for an extra screen.
7. If installation fails, read `%APPDATA%\DrawSplay\install.json`,
   `%APPDATA%\DrawSplay\host.log`, and `C:\Windows\INF\setupapi.dev.log`.
   Corporate App Control policy may require an administrator-approved signing path.

`-install -quiet` authorizes the same signing/trust changes without the disclosure
dialog. Add `-no-driver-install` to skip virtual-display installation. The
PowerShell install wrapper uses quiet mode; invoking it authorizes that behavior.

No certificate purchase or Microsoft account is needed for this local path.
The remaining sections are an alternative for Microsoft signing/certification,
not prerequisites for running the local-signing installer.

There is also a local test-signing route, but it can require changing boot
security settings and restarting the PC. This guide deliberately does not
tell you to disable Secure Boot or signature enforcement on your everyday PC.
Use a dedicated test machine and Microsoft's test-signing documentation if
you choose that route.

## A few words explained

| Word | What it means here |
| --- | --- |
| Driver | The software that lets Windows create DrawSplay's extra display. |
| DLL | `DrawSplayIdd.dll`: the actual driver program. |
| INF | `DrawSplayIdd.inf`: Windows' installation instructions for that driver. |
| Catalog / CAT | `DrawSplayIdd.cat`: a signed list of the package's file hashes. Changing the INF or DLL invalidates the catalog's coverage. |
| PDB | `DrawSplayIdd.pdb`: debugging symbols, needed for Microsoft's submission process. Keep it from the same build as the DLL. |
| Certificate | A verified signing identity, not the driver itself. |
| EV | Extended Validation: the certificate issuer checks your legal identity. |
| Partner Center | Microsoft's website for registering and submitting drivers. |
| CAB | A Windows archive containing the files you send to Microsoft. |
| CI | A build service such as GitHub Actions. |
| HSM | Hardware that protects your signing key, sometimes accessed through a cloud service. |

## Part 1: The one-time account and certificate setup

### Step 1: Check that you can qualify before paying

Open Microsoft's [Hardware Developer Program registration instructions](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/hardware-program-register).

Read the prerequisites. You will need a legal identity that the certificate
provider and Microsoft can validate, an organization/work account, and a legal
contact authorized to accept agreements.

If you are an individual without a registered business, ask Microsoft and
the certificate provider whether your particular legal status qualifies.
**Do this before buying anything.** Do not invent company details.

### Step 2: Choose an EV code-signing certificate

Start with the providers linked in Microsoft's
[certificate requirements](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/code-signing-reqs).
Examples include Certum, DigiCert, GlobalSign, Sectigo, and SSL.com.

Ask the provider these exact questions:

> I need an EV code-signing certificate for Microsoft Hardware Developer
> Program enrollment and Windows driver CAB submissions. Does this product
> support that? Can I use it unattended from GitHub Actions? What signing
> software and hardware or cloud service does it require? What are the
> certificate, hardware, subscription, and per-signature costs?

Do not buy an HTTPS/website certificate. That is a different product.
Do not assume a regular executable-signing service supports driver submissions.

For automation, prefer a supported cloud/HSM workflow that can sign without
someone pressing a USB-token button for every build. Ask how unattended
authentication works and how access is restricted.

**Do not assume you will receive an exportable `.pfx` file.** Modern publicly
trusted code-signing keys are commonly hardware-protected. DrawSplay's existing
PFX-based scripts may need adaptation to your provider.

### Step 3: Complete the provider's identity checks

Follow the provider's instructions, pay its fees, and complete its validation.
Install its signing client or token software.

Keep the signing key under your control. Never put private keys, token PINs,
passwords, or recovery material into this repository or a chat.

**Checkpoint:** the provider confirms that your certificate is active and you
can sign a sample file using its documented procedure.

### Step 4: Register with Microsoft

1. Open [Hardware Developer Program registration](https://partner.microsoft.com/dashboard/account/exp/enrollment/welcome?cloudInstance=Global&accountProgram=hardware).
2. Use your organization's Microsoft Entra work account. If you do not have
   one, follow Microsoft's **Create work account** route.
3. The person completing registration needs the required global-administrator
   access. This account-setup privilege is not a reason to give a CI job
   global-administrator privileges.
4. Select the **Hardware** program.
5. Enter accurate legal/company details and a legal contact who can receive
   external email.
6. Have the authorized person review and accept the agreements.
7. Follow **Manage certificates** to associate your EV certificate and
   complete the portal's proof-of-ownership instructions.
8. Respond to Microsoft's verification emails and business questionnaire.
9. Wait for account approval.

Only supply public certificate information and proof requested by the portal.
Do not upload your private key or PFX password as account-verification material.

**Checkpoint:** you can open the
[hardware dashboard](https://partner.microsoft.com/dashboard/hardware/Search)
and create a hardware submission.

An approved account must have a valid associated EV certificate when submitting.
Microsoft also permits a registered Authenticode certificate for individual
submissions under its documented rules; using your approved EV signing
workflow is the simpler starting point.

## Part 2: Build a package to send to Microsoft

Do one manual submission successfully before trying to automate everything.

### Step 5: Install the build tools

On your Windows development PC, install:

1. **Visual Studio 2022** or its Build Tools, with **Desktop development with C++**
   and the MSVC x64 toolset.
2. A compatible **Windows SDK**, including signing tools.
3. **NuGet CLI** (`nuget.exe`) from [nuget.org](https://www.nuget.org/downloads).
   Put its folder on PATH, then reopen PowerShell.
4. **Go 1.25 or newer** from [go.dev](https://go.dev/dl/), for rebuilding the host.
5. Your certificate provider's signing software.

The commands below assume the repository is `F:\Drawsplay`. Change that path
if your checkout is somewhere else. `C:\DrawSplaySigning` is a local working
folder outside the repository, not a place for private keys.

Open a normal PowerShell window and run:

```powershell
Set-Location F:\Drawsplay
New-Item -ItemType Directory -Force C:\DrawSplaySigning | Out-Null
go version
nuget help
```

If a command says it is not recognized, fix that tool's installation/PATH
before continuing. For every native build/signing command below, stop on an
error or nonzero exit code; do not continue with partial output.

### Step 6: Download the WDK build files

The Windows Driver Kit (WDK) contains the driver-specific headers and libraries.

```powershell
nuget install Microsoft.Windows.WDK.x64 `
  -OutputDirectory C:\DrawSplaySigning\wdk `
  -ExcludeVersion -NonInteractive
```

The expected WDK root for this package is:

```text
C:\DrawSplaySigning\wdk\Microsoft.Windows.WDK.x64\c
```

Confirm that it contains `Include` and `Lib` folders. If a future package
changes its layout, use the actual folder containing those folders.
Record the restored package version so later builds can pin the same version.

### Step 7: Build the driver with debugging symbols

```powershell
Set-Location F:\Drawsplay
.\drivers\windows\idd\build.ps1 `
  -WdkRoot C:\DrawSplaySigning\wdk\Microsoft.Windows.WDK.x64\c `
  -Arch x64 -Symbols `
  -OutDir C:\DrawSplaySigning\package
```

You should now have:

```text
C:\DrawSplaySigning\package\DrawSplayIdd.dll
C:\DrawSplaySigning\package\DrawSplayIdd.inf
C:\DrawSplaySigning\package\DrawSplayIdd.pdb
```

The build may also produce intermediate files; those do not belong in the CAB.
If the script reports a missing SDK, UMDF, or IddCx folder, stop and install a
compatible SDK/WDK rather than guessing new version values.

### Step 8: Locate the signing tools

In File Explorer, open:

```text
C:\Program Files (x86)\Windows Kits\10\bin
```

Open your installed SDK version folder, then `x64`. Find `stampinf.exe`,
`Inf2Cat.exe`, and `signtool.exe`. The WDK may supply the first two in its own
`bin` tree instead.

Set these variables to the **actual existing paths** on your PC:

```powershell
$stampinf = 'C:\replace-with-actual-folder\stampinf.exe'
$inf2cat = 'C:\replace-with-actual-folder\Inf2Cat.exe'
$signtool = 'C:\replace-with-actual-folder\signtool.exe'
```

Those are placeholders, not paths to copy unchanged.
PowerShell's `&` below means "run the program whose path is in this variable."

### Step 9: Stamp the INF and create its catalog

Pick a driver version newer than your previous submission. The example uses
`1.0.3.0`; increase it again for subsequent changed driver packages.

```powershell
& $stampinf -f C:\DrawSplaySigning\package\DrawSplayIdd.inf `
  -a amd64 -d (Get-Date -Format 'MM/dd/yyyy') -v 1.0.3.0

& $inf2cat /driver:C:\DrawSplaySigning\package /os:10_X64 /uselocaltime
```

`10_X64` matches the current repository script's target. Confirm the correct
target identifiers with the installed Inf2Cat documentation when changing OS
support; this command is not evidence of compatibility with every Windows version.

**Checkpoint:** `DrawSplayIdd.cat` exists and Inf2Cat completed successfully.
Do not modify the INF or DLL after generating the catalog.

### Step 10: Create the submission CAB

Open Notepad. Paste this text and save it as
`C:\DrawSplaySigning\submission.ddf`. In **Save as type**, choose **All files**
so Notepad does not append `.txt`.

```text
.OPTION EXPLICIT
.Set CabinetNameTemplate=DrawSplayIdd.cab
.Set DiskDirectoryTemplate=C:\DrawSplaySigning\cab
.Set Cabinet=on
.Set Compress=on
.Set CompressionType=MSZIP
.Set MaxDiskSize=0
.Set MaxCabinetSize=0
.Set DestinationDir=DrawSplayIdd
C:\DrawSplaySigning\package\DrawSplayIdd.inf
C:\DrawSplaySigning\package\DrawSplayIdd.dll
C:\DrawSplaySigning\package\DrawSplayIdd.cat
C:\DrawSplaySigning\package\DrawSplayIdd.pdb
```

Then run:

```powershell
makecab.exe /F C:\DrawSplaySigning\submission.ddf
expand.exe -D C:\DrawSplaySigning\cab\DrawSplayIdd.cab
```

Check that the archive contains those four files under `DrawSplayIdd`.
Microsoft requires the driver package to be in a subfolder, not at the CAB root.

### Step 11: Sign the CAB with your approved certificate

**Use your certificate provider's procedure.** A cloud/HSM provider may have
its own command or require extra SignTool options.

If the provider exposes the certificate through your current user's Windows
certificate store, a typical command looks like this:

```powershell
& $signtool sign /s My /sha1 'REPLACE_WITH_CERTIFICATE_THUMBPRINT' `
  /fd SHA256 /tr 'http://timestamp.digicert.com' /td SHA256 `
  C:\DrawSplaySigning\cab\DrawSplayIdd.cab

& $signtool verify /pa /v C:\DrawSplaySigning\cab\DrawSplayIdd.cab
```

Replace the thumbprint with the one your provider identifies. `/sha1` selects
the certificate by its thumbprint; `/fd SHA256` selects the file digest.
Use the provider's timestamp URL if it specifies another one.
This example is not a universal cloud-signing command.

**Checkpoint:** verification succeeds and identifies your intended publisher.
At this point you have signed the submission, not completed Microsoft signing.

## Part 3: Have Microsoft sign it

### Step 12: Submit the CAB

For the controlled-testing attestation route:

1. Open the [hardware dashboard](https://partner.microsoft.com/dashboard/hardware/Search).
2. Choose **Submit new hardware**.
3. Give the submission a recognizable name, such as
   `DrawSplay virtual display x64 1.0.3.0`.
4. Upload `C:\DrawSplaySigning\cab\DrawSplayIdd.cab`.
5. Select the appropriate Windows desktop signatures.
6. Follow the current [attestation instructions](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/code-signing-attestation).
   Microsoft's walkthrough leaves the two test-signing options unchecked;
   those options are distinct from the intended testing use of attestation.
7. Submit and wait for processing. If rejected, read the validation report,
   correct that specific problem, rebuild as necessary, and resubmit.
8. Download the **signed driver package**, not your original uploaded CAB.

For a retail/certified release, use the
[HLK getting-started instructions](https://learn.microsoft.com/en-us/windows-hardware/test/hlk/getstarted/windows-hlk-getting-started)
and the WHCP submission route instead. This adds a controller/test-machine
setup, applicable device tests, and an HLK result package. Merely clicking a
different portal checkbox does not replace the required testing.

### Step 13: Extract and check Microsoft's returned package

Extract the returned archive to `C:\DrawSplaySigning\returned`.
Locate the folder containing the matching `DrawSplayIdd.inf`, `.dll`, and `.cat`.
The exact nested folder structure depends on the returned download.

In the commands below, replace the example folder with that actual folder:

```powershell
$signedPackage = 'C:\DrawSplaySigning\returned\REPLACE_WITH_ACTUAL_PACKAGE_FOLDER'
& $signtool verify /pa /v "$signedPackage\DrawSplayIdd.cat"
& $signtool verify /pa /v /c "$signedPackage\DrawSplayIdd.cat" `
  "$signedPackage\DrawSplayIdd.inf"
& $signtool verify /pa /v /c "$signedPackage\DrawSplayIdd.cat" `
  "$signedPackage\DrawSplayIdd.dll"
```

Confirm the returned catalog has a valid Microsoft signing chain and covers
the returned files. Do not mix the returned catalog with the old local DLL
or INF. Do not edit any of the returned package files.

These checks establish signature validity and catalog membership. They do
not replace installing and exercising the package on the target Windows
versions with their normal security settings.

## Part 4: Put the signed package into DrawSplay

### Step 14: Copy the returned package into the host's embed folder

Back up the existing three files somewhere outside the repository first.
Then, using the `$signedPackage` variable from Step 13:

```powershell
Copy-Item "$signedPackage\DrawSplayIdd.inf" F:\Drawsplay\host\driver\idd -Force
Copy-Item "$signedPackage\DrawSplayIdd.dll" F:\Drawsplay\host\driver\idd -Force
Copy-Item "$signedPackage\DrawSplayIdd.cat" F:\Drawsplay\host\driver\idd -Force
```

DrawSplay embeds those exact three files when the Go host is built.
They contain no private signing key. Keep the matching symbols and submission
record in controlled build-artifact storage for debugging.

### Step 15: Rebuild the host without rebuilding the driver

```powershell
Set-Location F:\Drawsplay\host
go build -ldflags "-H=windowsgui" -o C:\DrawSplaySigning\DrawSplay.exe .
```

Stop if the build fails. A Git "dubious ownership" message is a separate
checkout-trust issue: verify that you trust the checkout before applying
Git's suggested per-directory exception.

**Important: do not run `scripts\build-release.ps1` at this point.**
It currently calls `scripts\build-idd-driver.ps1`, which can regenerate and
overwrite the Microsoft-signed driver files you just copied.

The current driver script can sign a catalog with your own PFX certificate.
It does **not** submit to Microsoft, download Microsoft's signature, or
preserve that returned package through a driver rebuild. A successful local
`signtool verify /pa` is not proof of successful installation.

Executable/installer Authenticode signing is a separate task. Use your
provider to sign the newly built `DrawSplay.exe` if you are distributing it;
signing the EXE does not replace signing the driver package.

### Step 16: Install the updated host

Save your work first: changing display drivers can briefly rearrange screens.
Run this from your normal user PowerShell and accept the installer's Windows
administrator approval prompt:

```powershell
Set-Location F:\Drawsplay
.\scripts\install-host-windows.ps1 `
  -SourceExe C:\DrawSplaySigning\DrawSplay.exe
```

Do not supply `-NoDriverInstall`. The standard installer creates the root
device as well as installing the package; `pnputil /add-driver` alone does
not necessarily create the virtual-display device.

If installation reports that a restart is required, save your work and restart.
Do not disable Windows security settings to turn a failed install into a pass.

### Step 17: Confirm that extend mode actually works

1. Press **Win + X**, choose **Device Manager**, and expand **Display adapters**.
2. Look for **DrawSplay Virtual Display** without a warning icon.
3. Open DrawSplay on the tablet and connect it.
4. Open **Settings > System > Display** on Windows. Look for an additional
   display. A monitor may only appear while a tablet session is connected.
5. Move a window onto that extra display and confirm it appears on the tablet.
6. Open `http://127.0.0.1:27180` and confirm the session is no longer reporting
   mirror fallback.

If it still mirrors, inspect:

```text
%APPDATA%\DrawSplay\host.log
%APPDATA%\DrawSplay\install.json
C:\Windows\INF\setupapi.dev.log
```

Paste the first path into File Explorer's address bar to open the log.
Search the Windows log for `DrawSplayIdd` and nearby signature/install errors.
Do not assume every remaining failure is signing: a correctly signed driver
can still fail to start or create a monitor.

## Part 5: Automate it after the manual process works

**This is the automation setup checklist, not an existing one-command feature.**
The repository does not currently implement Microsoft's submission/download
cycle or safe reuse of the returned driver in release packaging.

### Step 18: Set up an API identity

Follow Microsoft's [Hardware dashboard API instructions](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/dashboard-api):

1. Create a Microsoft Entra application for the signing automation.
2. Associate it with your approved Partner Center account.
3. Apply Microsoft's required account role and hardware submission permissions.
   Start with the permissions needed to submit drivers; add shipping-label
   permissions only if your automation needs them.
4. Record the tenant ID, application/client ID, and authentication credential.
5. Put credentials in GitHub **Settings > Secrets and variables > Actions**,
   preferably behind a protected release environment.
6. Configure the certificate provider's CI authentication separately.

An Entra API credential authorizes portal API calls. It does **not** replace
the code-signing certificate or sign a CAB by itself.

Do not paste secrets into workflow YAML. Do not expose signing jobs to
untrusted pull requests. Set credential-expiry reminders and a rotation process.

### Step 19: Implement the signing job

The job needs to do these operations in order:

1. Build a versioned driver and its matching PDB.
2. Generate the catalog and create the CAB.
3. Sign the CAB through your certificate provider.
4. Authenticate to the Hardware dashboard API.
5. Create the product/submission, upload the CAB, and commit the submission
   using Microsoft's documented API sequence.
6. Poll status with a timeout and report validation/rejection details.
7. Download the completed signed package.
8. Verify its signatures and catalog membership.
9. Save the returned package, symbols, hashes, source revision, tool versions,
   and submission ID as versioned artifacts.
10. Feed that exact signed package into host packaging **without regenerating
    or modifying it**.
11. Install and exercise it on representative Windows test machines.

Microsoft documents the API sequence under
[Manage product submissions](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/manage-product-submissions).
The certified-release route must also produce the required HLK results;
an API upload does not make testing optional.

The release pipeline must be changed to support a pre-signed-driver input
and skip driver regeneration for that input. Until that exists, do not assume
setting `DRAWSPLAY_REQUIRE_SIGNING=1` completes this process.

**You do not need another Microsoft submission for every host or Android
change.** Reuse the unchanged, verified signed driver package. Submit again
when its DLL, INF, or other covered content changes. Keep the account and
certificate valid for future submissions and maintenance.

### Step 20: Keep one human approval for releases

Automate the mechanical work, but protect production signing and publishing
with a release approval. Account registration, legal agreements, certificate
issuance/renewal, and Microsoft's approval decisions cannot be replaced by a
script or guaranteed to finish immediately.

## If you only do the next three things

1. Confirm your legal identity qualifies for an EV certificate and Hardware
   Developer Program account.
2. Choose a provider that explicitly supports Microsoft driver submissions
   and unattended CI signing.
3. Complete one manual Microsoft submission and verify extend mode before
   paying for or building more automation.

## Official references

- [Hardware Developer Program registration](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/hardware-program-register)
- [Certificate requirements](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/code-signing-reqs)
- [Signing options and attestation limitations](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/driver-signing-offerings)
- [Attestation CAB preparation and submission](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/code-signing-attestation)
- [Windows HLK getting started](https://learn.microsoft.com/en-us/windows-hardware/test/hlk/getstarted/windows-hlk-getting-started)
- [Hardware dashboard API setup](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/dashboard-api)
- [Product submission API workflow](https://learn.microsoft.com/en-us/windows-hardware/drivers/dashboard/manage-product-submissions)
- [DrawSplay executable and Android signing guide](../SIGNING.md)
