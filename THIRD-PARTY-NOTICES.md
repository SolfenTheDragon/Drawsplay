# Third-party notices

DrawSplay includes third-party components under their own licenses.

## DrawSplay IddCx virtual display driver (`drivers/windows/idd`)

DrawSplay's bundled Windows virtual display driver is derived from Microsoft's
IndirectDisplay / IddSampleDriver sample (part of the
[Windows driver samples](https://github.com/microsoft/Windows-driver-samples)),
which is licensed under the **Microsoft Public License (MS-PL)**.

- Copyright (c) Microsoft Corporation.
- License text: [`drivers/windows/idd/LICENSE`](drivers/windows/idd/LICENSE).

Per MS-PL section 3(C), the copyright and attribution notices from the original
sample are retained in the source. The compiled driver (`DrawSplayIdd.dll` and
its catalog) is distributed in object-code form under a license that complies
with the MS-PL, as required by section 3(D). DrawSplay's own modifications are
part of the DrawSplay project and licensed under the GPL-3.0 (see
[`LICENSE`](LICENSE)); the MS-PL continues to govern the portions derived from
the Microsoft sample.
