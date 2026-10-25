---
title: Onboarding and Ops Playbook
type: synthesis
tags: [synthesis, onboarding, operations, release]
created: 2026-08-12
updated: 2026-08-12
sources:
  - wiki/sources/readme-summary.md
  - wiki/sources/functionality-summary.md
  - wiki/sources/build-summary.md
  - wiki/sources/public-setup-summary.md
summary: Combined operational playbook for new maintainers spanning architecture, local development, release, and security posture.
---

# Onboarding and Ops Playbook

## 1. System mental model

Start with:

- [Architecture Overview](../topics/architecture-overview.md)
- [Transport and Session Model](../topics/transport-and-session-model.md)
- [Protocol Reference](../topics/protocol-reference.md)

## 2. Local development loop

1. Build and run host.
2. Build/install Android app.
3. Validate connectivity over USB default path.
4. Use web UI for runtime diagnostics and settings checks.

Reference:

- [BUILD Summary](../sources/build-summary.md)

## 3. Release and support loop

1. Package release artifacts.
2. Validate signed outputs and checksums.
3. Follow public setup flow for end-user repro.

Reference:

- [Public Setup Summary](../sources/public-setup-summary.md)

## 4. Security posture checkpoints

- Confirm transport security and secret-handling defaults.
- Review residual local API trust-boundary risks.
- Reassess after any transport, settings, or auth changes.

Reference:

- [Security Model](../topics/security-model.md)

