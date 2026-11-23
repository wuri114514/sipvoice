# Security Policy

## Supported versions

Only the latest release receives fixes. This project is alpha software; there are no long-term
support branches.

| Version | Supported |
|---|---|
| latest `v1.x` | ✅ |
| older | ❌ |

## Reporting a vulnerability

Please **do not open a public issue** for security problems.

Use GitHub's [private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing/privately-reporting-a-security-vulnerability)
on this repository (Security → Report a vulnerability). If that is unavailable, open an issue that
only says you would like a private channel and we will arrange one — do not include details.

Please include:

- affected version (`voicebridge -version`)
- a short description and impact
- reproduction steps, if you have them
- any suggested fix

We will acknowledge within a reasonable time and credit you in the release notes unless you prefer
to stay anonymous.

## Scope and known caveats

This is a voice appliance that runs on a private LAN. A few things are **by design** and should be
understood before reporting:

- **Default SIP credentials are public knowledge** (`1001/SipVoice1001`, `1002/SipVoice1002`). Change
  them after installation. Using the defaults on a reachable network is a misconfiguration, not a
  vulnerability.
- **The web console uses a self-signed certificate** by default; browsers will warn. You can import
  your own certificate from the console.
- **The console listens on `0.0.0.0:8080`** so you can reach it from the LAN. Put it behind a
  firewall / VLAN if you expose it more widely.
- **API keys are stored in `/etc/sipvoice/config.json`** (mode `0660`, group `sipvoice`). Protect
  that file like any other credential store.
- Do not expose the AudioSocket port (`127.0.0.1:9092`) or Asterisk (`5060/udp`) to the internet.

## What we do take seriously

Anything that lets an **unauthenticated** party:

- execute code, read files, or write outside `/etc/sipvoice`, `/opt/sipvoice`
- bypass console authentication or hijack a session
- trigger arbitrary outbound requests (SSRF) through provider/model fields
- escalate from the `sipvoice` service user to root

Reports in these areas are very welcome.
