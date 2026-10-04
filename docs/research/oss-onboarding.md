# How comparable self-host tools onboard first-run users

- **Ticket:** [Siddhj2206/pluto#24](https://github.com/Siddhj2206/pluto/issues/24) (Research: how comparable self-host tools onboard)
- **Map:** [Siddhj2206/pluto#20](https://github.com/Siddhj2206/pluto/issues/20) (Wayfinder: adoption route)
- **Date:** 2026-10-04
- **Method:** primary sources only (official docs, official source repositories, release pages and GitHub APIs). Every claim below is linked to the source that owns it; fetch dates are in [Sources checked](#sources-checked). Points that could not be pinned to a primary source are marked **Uncertainty**.

**Why this matters for pluto:** this ticket unblocks the install/packaging decision ([#21](https://github.com/Siddhj2206/pluto/issues/21)) and the docs/demo/support decision ([#23](https://github.com/Siddhj2206/pluto/issues/23)). The five subjects bracket the design space: Coolify is the mass-market self-hosting tool pluto is explicitly not; Home Assistant and Proxmox are the reference "no account, own your data" stories; Gitea is the closest single-binary README-to-running pattern; celld is the closest architectural cousin and the model for a guarantees page. None of this changes the locked wedge in [#20](https://github.com/Siddhj2206/pluto/issues/20): unattended durable work for technical self-hosters, no hosting service, no accounts, no telemetry, single-host mode fully useful.

---

## Resolution in brief

1. **Every tool ships one recommended first-run path on top of several install methods.** Home Assistant's index sends most users to HA OS while container installs are labelled "manually handle updates"; Proxmox recommends the ISO and calls the Debian-top install "only recommended for advanced users"; Coolify's page is a prerequisites checklist ending in one command; Gitea's binary page is copy-paste commands; celld's install is one `curl`. The README should present one canonical path, with the alternatives in a table below. ([HA installation](https://www.home-assistant.io/installation/), [Proxmox installation](https://pve.proxmox.com/wiki/Installation), [Coolify self-hosted](https://coolify.io/docs/start-with-self-hosted), [Gitea binary](https://docs.gitea.com/installation/install-from-binary), [celld install](https://celld.dev/docs))

2. **"No account" is a spectrum, and pluto wants the strict end.** Home Assistant and Proxmox need no account at all; HA's analytics are opt-in and off by default ("Sharing is disabled by default") and Proxmox's paid subscription is a support contract, not an identity. celld states "there is no account or join service" and its telemetry is opt-in, writing to the user's own bucket or collector (`CELLD_OTEL` default `0`). Gitea needs no account but its core phones `dl.gitea.com` weekly by default (`cron.update_checker`, `ENABLED=true`, `@every 168h`). Coolify self-hosted needs no external account, but ships anonymous telemetry on by default and reports errors to Sentry unless the toggle is flipped. pluto's no-telemetry promise should therefore be a code-level default — no background phone-home at all — with any update check an explicit user action. ([HA onboarding](https://www.home-assistant.io/getting-started/onboarding/), [celld telemetry](https://celld.dev/docs/telemetry), [Gitea config cheat sheet](https://docs.gitea.com/administration/config-cheat-sheet), [Coolify settings source](https://github.com/coollabsio/coolify/blob/main/resources/views/livewire/settings/advanced.blade.php), [Coolify Init.php](https://github.com/coollabsio/coolify/blob/main/app/Console/Commands/Init.php), [Coolify Handler.php](https://github.com/coollabsio/coolify/blob/main/app/Exceptions/Handler.php))

3. **Update mechanics split cleanly into "replace an artifact" and "run a system".** Gitea: replace the binary or `docker pull`, DB migrations run on startup, back up first. celld: re-run the installer with `CELLD_VERSION` pinned; roughly weekly releases with documented rolling-upgrade exceptions. Home Assistant: one-click update or `ha core update --backup`, monthly release train plus patches, beta channel via `--channel beta`. Coolify: UI auto-update with a cron frequency, a CDN `versions.json`, and update logs on disk. Proxmox: apt with repository channels (enterprise / no-subscription / test) and a checklist-driven in-place major upgrade. For pluto, the host agent and box images are separate artifacts: `pluto upgrade` must treat them separately, keep the previous host-agent version for rollback, and never auto-update by default. ([Gitea upgrade](https://docs.gitea.com/installation/upgrade-from-gitea), [celld docs](https://celld.dev/docs), [HA common tasks OS](https://www.home-assistant.io/common-tasks/os/), [Coolify update](https://coolify.io/docs/core/instance-management/update), [Proxmox package repositories](https://pve.proxmox.com/wiki/Package_Repositories), [Proxmox 8→9 upgrade](https://pve.proxmox.com/wiki/Upgrade_from_8_to_9))

4. **Docs topology: README-first, then a small set of guarantee-style pages.** Gitea and Home Assistant generate a docs site from a dedicated docs repository; Coolify keeps a separate `coolify-docs` repo; Proxmox separates a wiki from generated reference docs (single-page admin guide, PDF/ePub, man pages, API viewer); celld is the small-project extreme — markdown lives in `celld/docs/` and "Documentation is generated from `celld/docs/`". celld's page set (Overview, Cloudflare compatibility, Limitations, Security, Telemetry, Testing, What celld guarantees) is the closest template for pluto's deep writing. ([Gitea docs source](https://gitea.com/gitea/docs), [HA docs repo edit link](https://github.com/home-assistant/home-assistant.io/tree/current/source/getting-started/onboarding.markdown), [Coolify docs repo](https://github.com/coollabsio/coolify-docs), [pve-docs index](https://pve.proxmox.com/pve-docs/), [celld telemetry page footer](https://celld.dev/docs/telemetry))

5. **Support posture tracks who employs the maintainers.** Coolify: Discord support forum plus a two-person inbox, with a paid Cloud. HA: large community (forum, Discord, GitHub trackers) plus Nabu Casa Link/hardware with paid support. Gitea: community forum/Discord plus CommitGo Enterprise (SSO, audit logs, SLA) and Cloud; Enterprise follows a rolling release with no public per-version EOL matrix. celld: Deno-led, pull requests disabled, patches by email, no community venue. Proxmox: community forum/bugtracker/source/FAQ; paid subscriptions gate the enterprise repo and support. Pluto has no company behind it, so the honest posture is README + GitHub issues + a stated best-effort boundary — closer to celld than Coolify — with the contribution model decided in [#22](https://github.com/Siddhj2206/pluto/issues/22). ([Coolify support](https://coolify.io/docs/support), [HA help](https://www.home-assistant.io/help/), [Nabu Casa](https://www.nabucasa.com/), [Gitea products](https://about.gitea.com/products/gitea), [Gitea support & release policy](https://about.gitea.com/about/support-release-policy), [celld README](https://github.com/denoland/celld/blob/main/README.md), [Proxmox package repositories](https://pve.proxmox.com/wiki/Package_Repositories))

6. **What ships at install is the adoption tax.** Proxmox ships an entire Debian OS on an ISO; HA OS is a full OS image, and Home Assistant's onboarding page documents an ~700 MB download during preparation. Coolify requires root/sudo and installs a Docker stack. Gitea and celld ship a single binary. pluto's leaning — single binary + systemd user unit, rootless ([#21](https://github.com/Siddhj2206/pluto/issues/21), [#2](https://github.com/Siddhj2206/pluto/issues/2), [#10](https://github.com/Siddhj2206/pluto/issues/10)) — is the Gitea/celld side of this split, and the right one: KVM is the only host dependency and it is checked, not shipped. ([Proxmox installation](https://pve.proxmox.com/wiki/Installation), [HA onboarding](https://www.home-assistant.io/getting-started/onboarding/), [Coolify self-hosted](https://coolify.io/docs/start-with-self-hosted), [Gitea binary](https://docs.gitea.com/installation/install-from-binary), [celld install](https://celld.dev/docs))

7. **"First box in five minutes" is a content problem more than an install problem.** Install and boot are sub-minute once the binary and image are local ([#9](https://github.com/Siddhj2206/pluto/issues/9): Firecracker time-to-shell measured at ~0.5–1.8 s on the first host). The budget killers are the base-image download, a mandatory store or tailnet setup, and a README that asks the user to choose before anything has worked. Local-store default, a small prebuilt base image with resumable download, and one `pluto up`-style command inside an existing git repo are the concrete requirements (§8).

---

## 1. Coolify

**What it is, who it is for.** An open-source, self-hostable PaaS ("alternative to Vercel, Heroku & Netlify") that deploys apps, databases and 280+ one-click services as Docker containers on servers you connect; Apache-2.0, 62.5k stars at fetch time. ([repo](https://github.com/coollabsio/coolify))

**Install paths.** The recommended automated install is `curl -fsSL https://cdn.coollabs.io/coolify/install.sh | bash`, run as root or a sudo-capable user on a "fresh server" — minimum 2 CPU cores, 2 GB RAM, 10 GB disk — with Debian/Ubuntu, RHEL-family, SUSE, Arch, Alpine and 64-bit Raspberry Pi OS supported. A manual Docker install exists "for full control" and "works with existing Docker installations"; a Raspberry Pi path documents flashing 64-bit OS and enabling SSH first. The installer prints the next steps in the terminal when it finishes. ([Start with Self-hosted](https://coolify.io/docs/start-with-self-hosted))

**First run.** After the script, open `http://<server-ip>:8000` and create the admin account; the docs warn: "Create your admin account as soon as installation finishes. Anyone who reaches the registration page first can become the instance admin and gain root access to your server." The last step is to back up `/data/coolify/source/.env` (contains `APP_KEY`, needed for restores). An advanced install variant pre-creates the root account from `ROOT_USERNAME` / `ROOT_USER_EMAIL` / `ROOT_USER_PASSWORD` environment variables "so the registration page is never exposed". ([Start with Self-hosted](https://coolify.io/docs/start-with-self-hosted))

**Update mechanics.** Settings → Configuration → Updates in the dashboard. Automatic updates are configurable with a cron expression (`every_minute` … `yearly`; default daily at midnight), and a separate check frequency (default hourly) compares against the CDN's `versions.json`. Manual updates run from the dashboard or the terminal; update logs land under `/data/coolify/source/upgrade-*.log`. Release notes are the [GitHub releases](https://github.com/coollabsio/coolify/releases). Cadence is high: 44 releases between 2026-01-01 and 2026-09-18, i.e. roughly weekly (GitHub releases API). The current line is v4.3.x with v4.4-rc.1 as a prerelease. ([Update Coolify](https://coolify.io/docs/core/instance-management/update))

**Docs structure.** A first-party docs site at coolify.io/docs with Get Started / Core / Contribute sections, kept in a separate repository, [`coollabsio/coolify-docs`](https://github.com/coollabsio/coolify-docs) (last pushed 2026-09-30). Pages carry a "Copy Markdown" button. There are dedicated first-deploy pages ("Deploy your first app/database/service"). ([docs home](https://coolify.io/docs))

**Support posture.** Discord is "the best first stop for most self-hosted questions"; email `hi@coollabs.io` is for Cloud billing or dedicated support. The docs state the core team is "fewer than 5 people, so direct support cannot be guaranteed for every single issue", and email is handled by two people. ([Support](https://coolify.io/docs/support))

**No-account story.** A self-hosted instance needs no Coolify account — only a local admin user. Coolify Cloud (from $5/month, teams get their own subscription) is optional and the docs compare it honestly: "Self-hosted Coolify has no license fee", all current features free forever. ([Self Hosted vs Coolify Cloud](https://coolify.io/docs/core/selfhosted-cloud-comparison))

**Telemetry — the part pluto should not copy.** The Advanced settings page has a listbox labelled "Anonymous telemetry" whose helper text is "Control installation counting and error reports", with options Enabled/Disabled. The source shows this is on by default in two places: the instance-settings migration sets `do_not_track` default `false`, and startup only skips the "alive signal" when `do_not_track == false` is inverted (i.e. enabled by default when unset). Error reports go to Sentry from `app/Exceptions/Handler.php` unless `do_not_track` is set. ([advanced.blade.php](https://github.com/coollabsio/coolify/blob/main/resources/views/livewire/settings/advanced.blade.php), [migration](https://github.com/coollabsio/coolify/blob/main/database/migrations/2023_03_20_112814_create_instance_settings_table.php), [Init.php](https://github.com/coollabsio/coolify/blob/main/app/Console/Commands/Init.php), [Handler.php](https://github.com/coollabsio/coolify/blob/main/app/Exceptions/Handler.php))

**Copy:** the checklist-style prerequisites before the one-liner; printing the exact next steps from the installer; the "secure your admin account immediately" warning (pluto needs the equivalent for host access); opt-in update cadence as a setting; honest self-hosted-vs-cloud comparison table.

**Avoid:** enabled-by-default anonymous telemetry and error reporting; a root/sudo installer (pluto is rootless by decision); a registration page at all (the "first visitor becomes admin" race is a direct consequence of a web UI pluto does not have); sponsorship popups (`is_sponsorship_popup_enabled` in the same settings source); a cloud sibling that changes the support boundary.

---

## 2. Home Assistant

**What it is, who it is for.** A local-first smart-home hub that keeps working when the internet is down; the installation index is written for people who may never open a terminal, with "skills required" and "tools required" bullets per path. It offers its own hardware (Green/Yellow) as the easiest path. ([Installation](https://www.home-assistant.io/installation/))

**Install paths.** The index offers Home Assistant OS (recommended for most users): Green hardware, Raspberry Pi, ODROID, generic x86-64, and VMs on Linux/macOS/Windows; and Home Assistant Container on Raspberry Pi/Linux/macOS/Windows/NAS, which "doesn't include apps" and where users "manually handle updates". Only two installation types are presented in 2026: HA OS and HA Container. Thread and Z-Wave support is called out as controlled by apps, so Container loses them out of the box. ([Installation](https://www.home-assistant.io/installation/))

**First run.** Five browser-only steps: open the address (usually `http://homeassistant.local/`, container `:8123`); wait while "Home Assistant downloads the latest version of Home Assistant (about 700 MB)" behind the "Preparing Home Assistant" page; create the owner account (an admin that "will always be able to change everything", with no credential recovery) or restore a backup; set location; choose what to share. The sharing step states: "Sharing is disabled by default." Then Finish → default dashboard. ([Onboarding](https://www.home-assistant.io/getting-started/onboarding/))

**Update mechanics.** Update from Settings → Updates in the UI, or from the CLI: `ha core update --backup` (backup flag), `ha core update --version x.y.z` for a specific version (the example uses `2026.9.4`), `ha supervisor options --channel beta` + `ha core update` for the beta channel, and "Join beta" from the Update dialog's three-dot menu. The docs tell users to search the release notes for "Backward-incompatible changes" before updating. Auto-backup-before-update is a configurable default in the common tasks page. Release notes are monthly blog posts — 2026.9 on Sep 2, 2026.8 on Aug 5, 2026.7 on Jul 1, and so on — with patch releases (2026.9.4 current at fetch time). ([Common tasks — OS](https://www.home-assistant.io/common-tasks/os/), [Common tasks — general](https://www.home-assistant.io/common-tasks/general/), [Release notes blog](https://www.home-assistant.io/blog/categories/release-notes/))

**Docs structure.** home-assistant.io is generated from the [`home-assistant/home-assistant.io`](https://github.com/home-assistant/home-assistant.io) repo; every page has "Edit this page" + "Provide feedback" links (integration docs live in the same repo, e.g. `source/_integrations/analytics.markdown`). Developer documentation is separate at `developers.home-assistant.io`. The help page links the privacy policy, ToS, CLA, code of conduct and license. ([A page's edit link](https://github.com/home-assistant/home-assistant.io/tree/current/source/getting-started/onboarding.markdown), [Help](https://www.home-assistant.io/help/))

**Support posture.** Community: forum, Discord, GitHub issue trackers per component (Core, Frontend, Supervisor, OS, apps, docs), feature requests in GitHub Discussions. Commercial: Nabu Casa sells Home Assistant Link (remote access, backups, voice; formerly "Cloud", 31-day trial), hardware, and paid support via support.nabucasa.com; the company states the majority of profit funds the Home Assistant foundation. ([Help](https://www.home-assistant.io/help/), [Nabu Casa](https://www.nabucasa.com/))

**No-account story.** Core HA works with a local owner account only; no cloud identity. Analytics is an opt-in integration: "Nothing is sent from your installation unless you explicitly opt in", sharing is off at onboarding, the toggle lives in Settings → System → Analytics, and the payloads are documented (basic/usage/statistics/diagnostics). Link is optional. ([Analytics](https://www.home-assistant.io/integrations/analytics/), [Onboarding](https://www.home-assistant.io/getting-started/onboarding/))

**Copy:** the fixed first-light sequence with an explicit preparation step and what it downloads; "skills/tools required" labels; opt-in analytics with the payloads documented and a visible toggle; monthly release train with a beta channel and patch releases; release notes as first-class docs (pluto's equivalent is GitHub releases + a guarantees page).

**Avoid:** the 700 MB first-run download; OS-image-first packaging (pluto installs alongside the user's OS); app-store/discovery-driven onboarding (pluto has no UI); hardware sales and a device-integration docs surface that has no analogue.

---

## 3. Gitea

**What it is, who it is for.** "A painless self-hosted all-in-one software development service" (Git hosting, code review, packages, CI/CD), MIT-licensed, built as a single binary with SQLite baked in. The about page markets "Easy Installation and Configuration" and a "lightweight" footprint. ([Products — Gitea](https://about.gitea.com/products/gitea))

**Install paths.** Docs offer binary, package manager, source, Linux service, SELinux notes, Windows service, Docker (rootful and rootless), Podman Quadlet, Kubernetes, and cloud providers. The binary path is: download from `dl.gitea.com` for a version (`wget -O gitea https://dl.gitea.com/gitea/28.0.0/gitea-28.0.0-linux-amd64`), verify the signature (sigstore since v1.27, plus GPG), create a `git` system user, create `/var/lib/gitea/{custom,data,log}` and `/etc/gitea`, copy the binary to `/usr/local/bin`, and run it. Git ≥ 2.0 is required. The docs state "Gitea should not be run as root" (use `setcap` or a systemd capability instead). ([Binary install](https://docs.gitea.com/installation/install-from-binary), [Command line](https://docs.gitea.com/administration/command-line))

**First run.** `gitea web` serves the install wizard on port 3000; the config file at `/etc/gitea/app.ini` is temporarily writable so the web installer can write it, then should be made read-only. Headless/manual alternative: pre-write the config with `INSTALL_LOCK = true`, database details, and generated secrets (`gitea generate secret SECRET_KEY`, `INTERNAL_TOKEN`), and make the file read-only. The Docker path says: "Visit http://server-ip:3000 and follow the installation wizard." Admin creation can also be done from the CLI: `gitea admin user create --username myname --password asecurepassword --email me@example.com` (with `--admin`). ([Binary install](https://docs.gitea.com/installation/install-from-binary), [Docker install](https://docs.gitea.com/installation/install-with-docker), [Command line](https://docs.gitea.com/administration/command-line))

**Update mechanics.** Read the changelog for breaking changes, resolve deprecated config warnings shown in Site Administration (Gitea "may refuse to start in the following version" otherwise), back up (DB, config, `APP_DATA_PATH`, external storage), then replace the binary or `docker compose pull && docker compose up -d`; the database is migrated automatically on first launch. Patch versions within `a.b.x` are compatible; `a.b` → `a.c` cannot be downgraded without a backup. `contrib/upgrade.sh` automates the Linux steps, and `gitea doctor check` / `doctor recreate-table` diagnose and repair. ([Upgrade from an old Gitea](https://docs.gitea.com/installation/upgrade-from-gitea), [Command line](https://docs.gitea.com/administration/command-line))

**Cadence.** From the GitHub releases API: v28.0.0 (2026-09-29), v1.27.3 (2026-08-29), v1.27.0 (2026-07-13), v1.26.0 (2026-04-18), v1.25.3 (2025-12-18) — a minor release roughly every two months with patches in between; the docs version selector currently lists 29-dev, 28.0.0 and 1.27.3. **Uncertainty:** the release scheme dropped the `1.` prefix at 28.0.0; the rationale is not stated on the pages read. ([Releases](https://github.com/go-gitea/gitea/releases), [docs home version selector](https://docs.gitea.com/))

**Docs structure.** docs.gitea.com is a versioned Docusaurus site; its source lives in [`gitea.com/gitea/docs`](https://gitea.com/gitea/docs) (with `versioned_docs/version-28/...` markdown). Sections: Installation, Administration, Usage, Development, Help, plus API docs. ([Binary install footer](https://docs.gitea.com/installation/install-from-binary))

**Support posture.** Community: forum.gitea.com, Discord, Stack Overflow tag, Mastodon/Bluesky. Commercial (CommitGo, Inc.): Gitea Cloud (managed, 30-day trial banner) and Gitea Enterprise ("SSO, audit logs, and SLA support"). The public support & release policy says Enterprise is a rolling release with no per-version public EOL matrix; security fixes can ship out of band. ([Products](https://about.gitea.com/products/gitea), [Support & release policy](https://about.gitea.com/about/support-release-policy))

**No-account story.** No account with any upstream service is needed to run Gitea. The one default external call is the version update check: `cron.update_checker` is `ENABLED = true`, `SCHEDULE = @every 168h`, `HTTP_ENDPOINT = https://dl.gitea.com/gitea/version.json`. It can be disabled in `app.ini`; no other telemetry appeared in the config cheat sheet. ([Config cheat sheet §cron.update_checker](https://docs.gitea.com/administration/config-cheat-sheet))

**Copy:** single binary with embedded assets; signature verification documented as part of install; CLI admin path as an alternative to the web wizard; backup-before-upgrade ritual; automatic DB migrations with an explicit downgrade policy; `gitea doctor`; versioned docs.

**Avoid:** a default background phone-home of any kind (even a benign version check) — pluto's no-telemetry claim should mean zero unsolicited network calls; a web installer as the primary first-run (pluto is CLI/SSH-only); carrying a long multi-version compatibility matrix (pluto's host agent and images are separate artifacts).

---

## 4. celld

**What it is, who it is for.** Deno's open-source (Apache-2.0) daemon that runs Cloudflare Workers-format applications — Workers, Durable Objects, KV, Queues, D1, R2, Workflows, Cron, static assets — on your own machines, with long-term state in a bucket you own; 4,990 stars. It is the closest system to pluto studied anywhere in this map: bucket-lease ownership, no membership service, disk-state durability. ([README](https://github.com/denoland/celld/blob/main/README.md), [celld.dev/docs](https://celld.dev/docs))

**Install paths.** One command: `curl -fsSL https://celld.dev/install.sh | sh`. The installer keeps each release under `~/.local/lib/celld/releases` and points a symlink at the current one; `CELLD_VERSION` pins a tag; releases carry a GitHub Actions build attestation verifiable with `gh attestation verify`. A container image is published for Linux x86-64 and ARM64 (`ghcr.io/denoland/celld`). A fleet additionally needs an S3-compatible/GCS/Azure bucket with conditional writes, or nothing at all for `celld dev`. ([Install](https://celld.dev/docs), [README install](https://github.com/denoland/celld/blob/main/README.md#install))

**First run.** Two distinct first runs, and this is the pattern pluto should copy: `celld dev` starts "a local object store", deploys the application, and runs one node with no Docker and no cloud bucket ("It does not require Docker or a cloud bucket"), default listener `http://127.0.0.1:9876`; for a deployed fleet, `celld deploy . --bucket ... --endpoint ... --region ...` from a Wrangler project (examples include `examples/counter`), then start a node with the same bucket settings. Setup order is install → bucket credentials → deploy → start node. ([Develop locally](https://celld.dev/docs), [Deploy an application](https://celld.dev/docs))

**Update mechanics.** Re-run the installer, optionally with `CELLD_VERSION`; releases are roughly weekly (12 releases from v0.0.1 on 2026-08-02 to v0.6.1 on 2026-10-01). The docs specify rolling upgrades per version, including exceptions where a rolling update is forbidden (v0.1.0→v0.2.0, v0.3.0→v0.4.0, v0.5.1→v0.6.0) and version-pinning cautions for downgrades. ([Releases](https://github.com/denoland/celld/releases), [Shut down and roll out a node](https://celld.dev/docs))

**Docs structure.** In-repo markdown under `celld/docs/` (README, cloudflare-compat, guarantees, limitations, security, telemetry, testing, wasm, services/) rendered at celld.dev; the telemetry page ends with "Documentation is generated from `celld/docs/`." The deep page is [What celld guarantees](https://celld.dev/docs/guarantees). ([docs directory](https://github.com/denoland/celld/tree/main/docs), [Telemetry](https://celld.dev/docs/telemetry))

**Support posture.** Deno-led; the README states "Pull requests are disabled" (low-context AI patches), invites focused patches by email (`git format-patch` to the maintainer), and includes a CLA-style certification assigning rights to Deno Land Inc. No forum, Discord, or paid support is documented. ([README contributions](https://github.com/denoland/celld/blob/main/README.md))

**No-account story.** Explicit: "Every node discovers owners and peers from bucket leases; there is no account or join service." Telemetry is opt-in and self-hosted: `CELLD_OTEL=0` off by default; when enabled it writes Parquet to your own bucket or sends OTLP to your own collector. ([README](https://github.com/denoland/celld/blob/main/README.md), [Telemetry](https://celld.dev/docs/telemetry))

**Copy:** the two-mode first run (`dev` with a local store, then fleet) — pluto's analogue is local single-host first, bucket later; installer with version pinning and attestation; in-repo markdown docs generated to a site; opt-in telemetry pointed at infrastructure the user owns; explicit "guarantees", "limitations", "security" and "telemetry" pages; documented upgrade exceptions instead of generic "upgrade normally".

**Avoid:** pull requests closed and email-only contribution as the default posture (pluto's decision lives in [#22](https://github.com/Siddhj2206/pluto/issues/22), but a solo maintainer should prefer celld's honesty over Coolify's Discord load if forced); carrying a Cloudflare-compatibility surface as a second product; beta-grade breakage expectations without a stability statement (celld is v0.x; pluto should state what is stable).

---

## 5. Proxmox VE

**What it is, who it is for.** A Debian-based bare-metal virtualization platform (KVM + LXC) with a web management UI, used by sysadmins and homelabbers; the free product coexists with paid subscriptions. ([Installation](https://pve.proxmox.com/wiki/Installation), [Roadmap / Release History](https://pve.proxmox.com/wiki/Roadmap))

**Install paths.** The ISO installer is "the recommended method for new and existing users": it partitions with ext4/XFS/BTRFS (tech preview)/ZFS, installs a complete Debian GNU/Linux plus the PVE kernel and toolset, and offers graphical, terminal, serial-console, debug, automated and rescue modes. The docs warn: "All existing data on the selected drives will be removed during the installation process. The installer does not add boot menu entries for other operating systems." Installing on top of an existing Debian is "only recommended for advanced users because detailed knowledge about Proxmox VE is required." Minimum CPU requirement is 64-bit with VT-x/AMD-V, and arm64 support is limited to NVIDIA Grace Hopper/Vera. ([Installation](https://pve.proxmox.com/wiki/Installation), [FAQ](https://pve.proxmox.com/wiki/FAQ))

**First run.** After install, browse to `https://<ip>:8006` and log in as `root` (realm PAM) with the password chosen in the installer; the installation page's first steps are: "Upload your subscription key to gain access to the Enterprise repository. Otherwise, you will need to set up one of the public, less tested package repositories", then check IP/hostname, timezone and firewall. The GUI has a Node → Subscription panel to upload a key and generate a system report for support cases. ([Installation](https://pve.proxmox.com/wiki/Installation), [GUI docs](https://pve.proxmox.com/pve-docs/chapter-pve-gui.html))

**Update mechanics.** apt-based. Package repositories come in three tiers: `pve-enterprise` ("the recommended repository and available for all Proxmox VE subscription users", requires a valid key), `pve-no-subscription` ("you do not need a subscription key", suitable for testing and non-production), and `pvetest` ("primarily used by developers"). Major upgrades are supported in place: the 8→9 guide steps through prerequisite checks, the `pve8to9` checklist script, repository migration, `apt` dist-upgrade, and reboot, with warnings to back up and expect downtime. Support lifetime: "Proxmox VE versions are supported at least as long as the corresponding Debian version is supported by the Debian Security Team, i.e. approximately 3 years after its initial release"; the FAQ's support table pairs PVE 9 with Debian 13 (Trixie, first release 2025-08), PVE 8 with Debian 12. Release history from the wiki: PVE 9.2 (2026-05-21), the arm64 variant of 9.2 (2026-08-05), 9.1 (2025-11-19), 9.0 (2025-08-05), and the 8.x line before it. ([Package repositories](https://pve.proxmox.com/wiki/Package_Repositories), [Upgrade 8→9](https://pve.proxmox.com/wiki/Upgrade_from_8_to_9), [FAQ](https://pve.proxmox.com/wiki/FAQ), [Roadmap](https://pve.proxmox.com/wiki/Roadmap))

**Docs structure.** Two surfaces: a community wiki (Installation, Package Repositories, Upgrade guides, FAQ, Roadmap/Release History) and generated reference docs at pve.proxmox.com/pve-docs — a single-page HTML administration guide plus PDF and ePub, per-chapter HTML (Introduction … FAQ … Bibliography), man pages for tools (`pct`, `pvenode`, `pvecm`, …), and an API viewer. The docs index footer is `support@proxmox.com`. ([pve-docs index](https://pve.proxmox.com/pve-docs/), [wiki](https://pve.proxmox.com/wiki/Main_Page))

**Support posture.** Community: the wiki sidebar links a support forum, bug tracker, source code and FAQ. Commercial: subscriptions (Basic/Standard/Premium; see pricing) gate the enterprise repository and support; "subscription keys are bound to the host architecture" (an arm64 key is not valid on x86 and vice versa). ([Package repositories](https://pve.proxmox.com/wiki/Package_Repositories), [Pricing](https://www.proxmox.com/en/proxmox-virtual-environment/pricing))

**No-account story.** No account is required for the free product; the subscription key is a support contract, and the free path is the no-subscription repository. No telemetry or phone-home is documented in the sources read (**Uncertainty:** not exhaustively audited across all PVE packages; the claim is only "none documented"). ([Package repositories](https://pve.proxmox.com/wiki/Package_Repositories), [Installation](https://pve.proxmox.com/wiki/Installation))

**Copy:** "recommended vs advanced" install framing; a support matrix tied to a base-OS lifecycle (pluto's analogue: Linux + KVM + x86_64 first, decline legibly); release history/changelog as part of the docs; repository-channel thinking for images (stable vs beta/test); a first-run checklist.

**Avoid:** an installer that replaces the user's OS; a paid tier gating the good update channel (pluto's enterprise-repo/no-subscription split must not exist); splitting docs between wiki and generated guide (one markdown source rendered once is celld's cheaper model); bare-metal root administration as the expected posture.

---

## 6. Cross-cutting patterns

| | Coolify | Home Assistant | Gitea | celld | Proxmox VE |
|---|---|---|---|---|---|
| **Install unit** | Docker stack via root script | OS image or container | Single binary (or Docker) | Single binary (or container) | Whole-OS ISO |
| **Install root needed?** | Yes (root/sudo) | OS image: yes; container: Docker group | No (`setcap`/systemd for :80) | No (user-local) | Yes (bare metal) |
| **First run** | Web dashboard, local admin | Browser wizard, local owner | Web installer at `:3000` or CLI admin | CLI: `celld dev` / `deploy` + node | Web UI `:8006`, `root@pam` |
| **First-run download** | Docker images | ~700 MB HA core | Binary (~100 MB class; not stated) | Binary; bucket optional for dev | Full ISO |
| **Update** | UI auto/manual, CDN versions.json | UI one-click / `ha core update --backup` | Replace binary/`docker pull`, auto DB migrate | Re-run installer with pinned tag | apt + repo channels |
| **Cadence** | ~weekly (44 in 2026 to Sep) | Monthly + patches | ~bi-monthly minor + patches | ~weekly | Feature releases per Debian cycle |
| **Docs** | Separate docs repo + site | Site generated from docs repo | Versioned docs repo + site | In-repo markdown → site | Wiki + generated guide/PDF/man/API |
| **Community support** | Discord + small team email | Forum, Discord, GitHub | Forum, Discord, Stack Overflow | None documented (email patches) | Forum, bug tracker, source, FAQ |
| **Commercial sibling** | Coolify Cloud ($5/mo) | Nabu Casa Link + hardware | Gitea Cloud + Enterprise (SLA) | None | Subscriptions (repo + support) |
| **Account needed** | No (local admin only) | No | No | No ("no account or join service") | No |
| **Telemetry default** | On (opt-out) | Off (opt-in) | Version check weekly (opt-out) | Off (opt-in, own bucket) | None documented |

Three patterns worth naming:

- **Install surfaces are separate from the first success surface.** Coolify installs Docker internals but the first success is a dashboard login; Gitea installs a binary but the first success is a web repo; celld installs a binary and the first success is `celld dev` printing a local URL. pluto's first success should be an interactive shell in a box, and nothing before that should require a decision.
- **No-account products still phone home unless the code is designed not to.** Gitea and Coolify both default to some outbound signal; HA and celld default to none. pluto's claim should be enforced by architecture (local store, no background checks).
- **Update channels are a documented product surface, not a footnote.** Every project says which versions are safe to mix and what breaks; celld is the model for pluto because its artifacts (node binary, bucket format) resemble pluto's (host agent, box images, disk-state format).

---

## 7. What pluto should copy, and what it should not

Given the locked constraints — no hosting service, no accounts, no telemetry, single-host fully useful, technical audience, BuildStream never a user requirement:

**Copy (with the source):**

1. **celld's install script contract** — one `curl | sh`, `PLUTO_VERSION`-style pinning, attestation verification, installer keeps versioned releases and one symlink, re-running the installer is the upgrade. ([celld install](https://celld.dev/docs))
2. **celld's two-mode first run** — local store first (`celld dev` requires no bucket), bucket/fleet later as an upgrade. pluto's version: `pluto up` works with local disk state; `pluto store add s3://...` is an optional later step. ([celld](https://celld.dev/docs))
3. **celld's docs page set** — in-repo markdown, generated once: Overview, Limitations, Security, Telemetry, Testing, Guarantees. pluto's "guarantees" page is where the disk-only pause contract, lease model and bucket requirements live. ([celld docs dir](https://github.com/denoland/celld/tree/main/docs))
4. **celld's telemetry posture** — off by default; if ever enabled, the sink is the user's own store. pluto goes further: no telemetry at all, stated on the box and in the docs (per [#23](https://github.com/Siddhj2206/pluto/issues/23)).
5. **Gitea's single-binary + CLI-admin + doctor pattern** — first run is a command, not a wizard; `pluto doctor` checks KVM, arch, user namespaces, git, systemd user units; sigstore-attested binaries. ([Gitea binary](https://docs.gitea.com/installation/install-from-binary), [sigstore section](https://docs.gitea.com/installation/install-from-binary))
6. **Gitea's backup-before-upgrade and migration-on-start ritual** — `pluto upgrade` should back up (or tell the user exactly what to back up), migrate state on first start, and keep the old binary until the new one reports healthy. ([Gitea upgrade](https://docs.gitea.com/installation/upgrade-from-gitea))
7. **HA's fixed first-light sequence and release discipline** — a small number of steps with a named wait state, a beta channel as an explicit opt-in, and release notes as a first-class artifact. pluto's beta channel: image tags (`base@beta`) and a `pluto upgrade --channel beta`. ([HA onboarding](https://www.home-assistant.io/getting-started/onboarding/), [HA OS updates](https://www.home-assistant.io/common-tasks/os/))
8. **Coolify's checklist install page + printed next steps** — prerequisites in a table, then one command, and the installer prints exactly what to do next. ([Coolify](https://coolify.io/docs/start-with-self-hosted))
9. **Proxmox's no-account free product and support-matrix discipline** — free path is the default path; the supported-setup table is honest; unsupported setups are declined in writing. ([Proxmox FAQ support table](https://pve.proxmox.com/wiki/FAQ), [Package repositories](https://pve.proxmox.com/wiki/Package_Repositories))

**Do not copy:**

1. **Enabled-by-default telemetry / error reporting** (Coolify) — `do_not_track` defaults to enabled; pluto's promise must be the default and the code path.
2. **A default weekly phone-home** (Gitea's update checker) — an update check, if any, should be an explicit `pluto upgrade --check`, never a background cron.
3. **Root/sudo or Docker requirements** (Coolify, HA Container) — pluto is rootless by decision ([#2](https://github.com/Siddhj2206/pluto/issues/2), [#10](https://github.com/Siddhj2206/pluto/issues/10)); the installer should refuse gracefully when `/dev/kvm` is unusable, not escalate.
4. **A web registration/onboarding surface** (Coolify's stranger-becomes-admin race, Gitea's web installer, HA's browser wizard) — pluto's first-run credential step is SSH keys + `pluto doctor`, and there is no port an attacker can claim first.
5. **Replacing the user's OS** (Proxmox ISO, HA OS) — pluto must be installable on the daily-driver machine it is meant for.
6. **A paid updater channel or cloud sibling** (Coolify Cloud, Gitea Enterprise, Proxmox enterprise repo, Nabu Casa) — out of scope by the map; it also keeps support expectations honest.
7. **A 700 MB first-run download** (HA) — the five-minute budget dies there; the base image must be small and resumable.
8. **Pull requests closed / email-only contributions as an ideology** (celld) — a defensible solo-maintainer choice, but pluto's contribution model should be decided in [#22](https://github.com/Siddhj2206/pluto/issues/22) on its own terms, with the celld trade-off on record.

---

## 8. "First box in five minutes" — what it actually requires

**Target:** on the reference host class (Linux, x86_64, KVM, `git` already installed, no sudo), a developer reads the README, runs one install command, then from inside a repo they already have runs one more command and lands in an interactive shell in a box that survives pause/resume. No bucket, no tailnet, no BuildStream, no Docker, no web page.

**Time budget (to be verified once the artifacts exist):**

| Step | Budget | What makes or breaks it |
|---|---|---|
| Host-agent install (single binary + systemd user unit) | 15–30 s | static binary; no package manager; `pluto doctor` prints the checks it ran ([#21](https://github.com/Siddhj2206/pluto/issues/21)) |
| Base box image acquisition | 30–120 s | prebuilt, content-addressed, resumable; must fit comfortably under ~1 GB; cached between boxes; never built locally |
| `pluto up` in an existing repo | < 10 s to the boot trigger | worktree-scoped box creation; no prompts; sensible defaults; cloud-init/NoCloud per-boot config ([#8](https://github.com/Siddhj2206/pluto/issues/8)) |
| Boot + attach | < 5 s | measured boot 0.43–1.8 s on the first host ([#9](https://github.com/Siddhj2206/pluto/issues/9)); attach over vsock |
| **Total** | **~2–3 min typical** | the 5-minute claim fails only on slow image downloads |

**Must ship at install time (nothing else):**

1. The host agent binary and its systemd user unit; no root; `/dev/kvm` and user-namespace check; x86_64 check; `git` check. The checks and their results must be visible in `pluto doctor` output, modeled on celld's storage test (`celld diagnose`) and Gitea's `doctor`. ([#21](https://github.com/Siddhj2206/pluto/issues/21), [celld guarantees](https://celld.dev/docs/guarantees), [Gitea command line](https://docs.gitea.com/administration/command-line))
2. One small prebuilt base image (shell, git, ssh/agent plumbing), distributed as a versioned artifact. BuildStream stays the factory ([#20](https://github.com/Siddhj2206/pluto/issues/20)); users never run it.
3. A local disk-state store default under the user's home or `$XDG_STATE_HOME`; bucket configuration is an optional upgrade, matching celld's `dev`-without-bucket behavior. ([celld](https://celld.dev/docs))
4. `pluto up`, `pluto status`, `pluto pause`, `pluto resume`, `pluto attach`, `pluto doctor` — enough verbs that the first session ends with a resumable box.
5. A README quickstart that is exactly the five-minute path, with expected output; alternatives (bucket, tailnet, systemd linger, other distros) in a "Going further" section below. The docs decision in [#23](https://github.com/Siddhj2206/pluto/issues/23) owns the final skeleton.

**Must not ship at install time:** an update check that runs unbidden; a bucket prompt; a tailnet prompt; a web UI; a package-manager dependency; a "choose your path" fork before the first box runs; any telemetry.

**Failure legibility.** Every first-run failure class should have a named outcome and a next step, because the audience will hit them: `/dev/kvm` absent or not writable (message + distro hint), user namespaces disabled, unsupported arch, no git repo in the current directory, image download interrupted (resume command), systemd user session absent (run in foreground or enable linger). This mirrors Proxmox's "recommended vs advanced" honesty and Coolify's prerequisites table rather than a generic stack trace.

**Open dependency:** the actual base-image size is unmeasured; the FSDK-derived image from [#7](https://github.com/Siddhj2206/pluto/issues/7) has no published artifact yet. The five-minute claim should be treated as a budget to validate at first build, not a promise in the README until then.

---

## 9. Open questions and uncertainty

1. **Base image size and acquisition** — the five-minute budget depends on it; nothing has been built yet. Validate at first image export ([#7](https://github.com/Siddhj2206/pluto/issues/7)).
2. **Update semantics** — host agent vs images, version pinning, rollback, whether `--channel beta` exists; belongs to [#21](https://github.com/Siddhj2206/pluto/issues/21). Recommendation from this research: separate commands, no auto-update by default, celld-style pinned installer for the agent.
3. **Any network call at all** — even an explicit `pluto upgrade --check` should be opt-in and documented; the guarantee should be testable (e.g. a CI check that the daemon makes no outbound request when idle).
4. **Docs site vs repo-only** — [#23](https://github.com/Siddhj2206/pluto/issues/23) owns it; celld proves in-repo markdown + a generated site is enough for a small project, while Gitea/HA show the cost of a separate docs repo.
5. **Contribution model vs support load** — [#22](https://github.com/Siddhj2206/pluto/issues/22) owns it; celld's PR closure and Coolify's Discord burden are the two failure modes, Gitea's forum/Discord is the mature middle.
6. **Uncertainty — Gitea version scheme:** the `1.` prefix disappears at 28.0.0; rationale unverified.
7. **Uncertainty — Coolify telemetry payloads:** traced to two call sites (Sentry error reports, install "alive signal") plus the settings label; the exact payload contents were not audited.
8. **Uncertainty — Proxmox telemetry:** "none documented" in the sources read; not an exhaustive audit of PVE packages, and the paid-subscription nag behavior was not reproduced from primary docs.
9. **Uncertainty — celld stability:** v0.x with weekly releases; the README does not call itself beta, but the release cadence and versioning imply pre-1.0. Treat its operational advice as directional, not as a stability guarantee.

---

## Sources checked

Fetched 2026-10-04 unless noted. All links are first-party: official docs, official repositories, official release pages, or first-party product pages.

**Coolify**
- [Start with Self-hosted](https://coolify.io/docs/start-with-self-hosted) — requirements, install script, first admin, secrets, advanced root-user install
- [Update Coolify](https://coolify.io/docs/core/instance-management/update) — auto/manual updates, frequencies, CDN versions.json, logs
- [Support](https://coolify.io/docs/support) — Discord, email, team size, expectations
- [Self Hosted vs Coolify Cloud](https://coolify.io/docs/core/selfhosted-cloud-comparison) — feature parity, $5/month Cloud, responsibility boundary
- [coolify-docs repo](https://github.com/coollabsio/coolify-docs) — docs live outside the main repo
- [advanced.blade.php](https://github.com/coollabsio/coolify/blob/main/resources/views/livewire/settings/advanced.blade.php) — "Anonymous telemetry" toggle, registration toggle, sponsorship popup
- [Init.php](https://github.com/coollabsio/coolify/blob/main/app/Console/Commands/Init.php) — alive signal unless disabled
- [Handler.php](https://github.com/coollabsio/coolify/blob/main/app/Exceptions/Handler.php) — Sentry error reporting unless disabled
- [instance settings migration](https://github.com/coollabsio/coolify/blob/main/database/migrations/2023_03_20_112814_create_instance_settings_table.php) — `do_not_track` default `false`
- [Releases](https://github.com/coollabsio/coolify/releases) and GitHub releases API — 44 releases in 2026 through 2026-09-18; v4.4-rc.1

**Home Assistant**
- [Installation](https://www.home-assistant.io/installation/) — HA OS vs Container, skills/tools, apps/Thread/Z-Wave caveats
- [Onboarding](https://www.home-assistant.io/getting-started/onboarding/) — five steps, ~700 MB download, owner account, sharing disabled by default
- [Analytics](https://www.home-assistant.io/integrations/analytics/) — opt-in, payloads, retention, toggle path
- [Common tasks — OS](https://www.home-assistant.io/common-tasks/os/) — UI/CLI updates, `--backup`, `--version`, beta channel
- [Common tasks — general](https://www.home-assistant.io/common-tasks/general/) — backup-before-update default
- [Release notes category](https://www.home-assistant.io/blog/categories/release-notes/) — monthly cadence since 2015
- [Help](https://www.home-assistant.io/help/) — forum, Discord, issue trackers
- [Nabu Casa](https://www.nabucasa.com/) — optional Link service, hardware, paid support, non-profit funding
- [onboarding.markdown edit link](https://github.com/home-assistant/home-assistant.io/tree/current/source/getting-started/onboarding.markdown) — docs source repo pattern

**Gitea**
- [Installation from binary](https://docs.gitea.com/installation/install-from-binary) — download, sigstore/GPG verification, directories, run, update, troubleshooting
- [Installation with Docker](https://docs.gitea.com/installation/install-with-docker) — compose, startup, web installer at :3000, upgrading
- [Upgrade from an old Gitea](https://docs.gitea.com/installation/upgrade-from-gitea) — changelog, deprecated options, backups, migrations, compatibility rules
- [Gitea Command Line](https://docs.gitea.com/administration/command-line) — `web`, `admin user create --admin`, `doctor`, `generate secret`
- [Config cheat sheet](https://docs.gitea.com/administration/config-cheat-sheet) — `cron.update_checker` defaults and endpoint
- [Releases](https://github.com/go-gitea/gitea/releases) and GitHub releases API — v28.0.0 … v1.25.3 dates
- [Products — Gitea](https://about.gitea.com/products/gitea) — MIT, positioning, CommitGo commercial support
- [Support & Release Policy](https://about.gitea.com/about/support-release-policy) — Enterprise rolling release, SLA

**celld**
- [celld README](https://github.com/denoland/celld/blob/main/README.md) — architecture, install, container, no account, contributions, license
- [celld.dev/docs](https://celld.dev/docs) — install, bucket configuration, deploy, `celld dev`, nodes, upgrades
- [celld.dev/docs/telemetry](https://celld.dev/docs/telemetry) — opt-in, bucket/OTLP sinks, docs generated from `celld/docs/`
- [docs directory](https://github.com/denoland/celld/tree/main/docs) — guarantee/limitation/security/telemetry pages
- [Releases](https://github.com/denoland/celld/releases) and GitHub releases API — v0.0.1 (2026-08-02) → v0.6.1 (2026-10-01), Apache-2.0
- [guarantees](https://celld.dev/docs/guarantees) — bucket contract, storage test, ownership

**Proxmox VE**
- [Installation](https://pve.proxmox.com/wiki/Installation) — ISO modes, data-loss warning, root password/email, post-install UI steps
- [Package Repositories](https://pve.proxmox.com/wiki/Package_Repositories) — enterprise vs no-subscription vs test, key requirements, architecture-bound keys
- [Upgrade from 8 to 9](https://pve.proxmox.com/wiki/Upgrade_from_8_to_9) — in-place apt upgrade, `pve8to9`, backup/downtime warnings
- [FAQ](https://pve.proxmox.com/wiki/FAQ) — CPU requirements, support lifetime, Debian pairing, support table
- [Roadmap / Release History](https://pve.proxmox.com/wiki/Roadmap) — PVE 9.2 (2026-05-21), arm64 9.2 (2026-08-05), 9.1 (2025-11-19), 9.0 (2025-08-05) back through 8.0
- [pve-docs index](https://pve.proxmox.com/pve-docs/) — admin guide HTML/PDF/ePub, per-chapter docs, man pages, API viewer
- [GUI chapter](https://pve.proxmox.com/pve-docs/chapter-pve-gui.html) — Node → Subscription panel
- [Pricing](https://www.proxmox.com/en/proxmox-virtual-environment/pricing) — subscription levels

**pluto internal**
- [Map #20](https://github.com/Siddhj2206/pluto/issues/20) — adoption route, locked audience and constraints
- [Issue #21](https://github.com/Siddhj2206/pluto/issues/21) — install, packaging and updates grilling
- [Issue #22](https://github.com/Siddhj2206/pluto/issues/22) — licensing and contribution model
- [Issue #23](https://github.com/Siddhj2206/pluto/issues/23) — docs, demo and support matrix
- [Issue #9](https://github.com/Siddhj2206/pluto/issues/9) — boot-path measurement on the first host (`docs/research/boot-path-check.md`, commit `220020c`)
- [Issue #2](https://github.com/Siddhj2206/pluto/issues/2) — Firecracker/VMM landscape and rootless verification
- [Issue #10](https://github.com/Siddhj2206/pluto/issues/10) — runner choice: rootless `unshare -Urn` is a hard requirement
- [Issue #7](https://github.com/Siddhj2206/pluto/issues/7) — BuildStream image pipeline
