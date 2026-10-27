# zen-backup-restore

[![Build Status](https://github.com/kishaningithub/zen-backup-restore/actions/workflows/build.yml/badge.svg)](https://github.com/kishaningithub/zen-backup-restore/actions/workflows/build.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/kishaningithub/zen-backup-restore)](https://goreportcard.com/report/github.com/kishaningithub/zen-backup-restore)
[![Latest release](https://img.shields.io/github/release/kishaningithub/zen-backup-restore.svg)](https://github.com/kishaningithub/zen-backup-restore/releases)

Back up and restore [Zen Browser](https://zen-browser.app/) profiles to local `.tar.gz` archives, with no account and no cloud sync. It's a single Go binary for macOS, Linux and Windows, inspired by [Zen-Backup-Tool](https://github.com/Ronin-CK/Zen-Backup-Tool).

## Installation

### Using Homebrew (Mac and Linux)

```bash
brew trust kishaningithub/tap
brew install --cask kishaningithub/tap/zen-backup-restore
```

### Using Scoop (Windows)

```powershell
scoop bucket add kishaningithub https://github.com/kishaningithub/scoop-bucket
scoop install kishaningithub/zen-backup-restore
```

### Using pkgx

```bash
pkgx zen-backup-restore@latest list
```

or if you prefer the eval style which only adds zen-backup-restore to your current shell session

```bash
eval $(pkgx +zen-backup-restore@latest)

zen-backup-restore list
```

### Using docker

Backups record absolute profile paths, so the container must see your home directory at the **same path** as the host:

```bash
alias zen-backup-restore='docker run --rm -it --user "$(id -u):$(id -g)" -e HOME="$HOME" -v "$HOME:$HOME" ghcr.io/kishaningithub/zen-backup-restore:latest'

zen-backup-restore list
```

On Linux, Zen's data dir is auto-detected. On macOS, pass it explicitly, since the container runs Linux:

```bash
zen-backup-restore backup --zen-dir "$HOME/Library/Application Support/zen"
```

> The "Zen is running" check may not work across the Docker VM on macOS, so make sure Zen is closed.

### Using go

```bash
go install github.com/kishaningithub/zen-backup-restore@latest
```

### Others

Head over to the [releases page](https://github.com/kishaningithub/zen-backup-restore/releases) and download a binary (or a `.deb`, `.rpm` or `.apk` package) for your platform.

## Usage

```sh
zen-backup-restore                     # interactive menu
zen-backup-restore list                # profiles + existing backups
zen-backup-restore backup              # back up the default profile
zen-backup-restore backup -p "Default (release)" --slim -o ~/Dropbox/zen
zen-backup-restore backup --exclude-sensitive   # no passwords, cookies or site data
zen-backup-restore restore             # pick a backup and a target interactively
zen-backup-restore restore backup.tar.gz --new "Restored"   # restore into a new profile
zen-backup-restore restore backup.tar.gz -p "Default (release)" -y   # overwrite a profile
```

Run `zen-backup-restore --help` (or `<command> --help`) for all flags. Shell completion scripts are available via `zen-backup-restore completion bash|zsh|fish|powershell`.

Backups go to `~/Backups/ZenBrowser` by default. Change this with `--backup-dir` or `ZEN_BACKUP_DIR`.
Zen's data dir is auto-detected (including Flatpak and Snap on Linux). Override it with `--zen-dir`. Profiles stored outside it, such as the "Scoop" profile created by `scoop install extras/zen-browser`, are picked up from `profiles.ini`.

**Quit Zen before backing up or restoring.** The tool checks Zen's profile lock and refuses to run while the profile is in use.

## What's backed up

The whole profile, including bookmarks, history, passwords (`key4.db` + `logins.json`), cookies, extensions and their data, workspaces, sessions, pinned tabs, containers, keyboard shortcuts, Zen mods/themes, `chrome/` and prefs. Only things Zen regenerates are excluded:

- caches (`cache2`, `startupCache`, …)
- lock files
- crash reports and telemetry
- downloaded DRM/codec plugins (`gmp-*`)
- SQLite `-shm` files

SQLite `-wal` files **are** included, so recent history isn't lost.

`--slim` makes a much smaller backup (often >1 GB smaller) by also skipping data that sites can re-download and old session snapshots:

- website storage (`storage/default/http*`): each site's IndexedDB, localStorage and offline caches
- Zen's rolling session snapshots (`zen-sessions-backup/`)
- Firefox's previous-session, per-update and previous crash-recovery snapshots (`sessionstore-backups/previous.jsonlz4`, `upgrade.jsonlz4-*`, `recovery.baklz4`), plus the crash-recovery copy `recovery.jsonlz4` when Zen was closed cleanly (it then restores from `sessionstore.jsonlz4`)
- the daily automatic bookmark backups (`bookmarkbackups/`); bookmarks themselves are kept in `places.sqlite`
- site icons (`favicons.sqlite`), refetched as you browse
- downloaded blocklists and certificate revocation data (Remote Settings cache in `storage/permanent/chrome`, `security_state/`), re-downloaded automatically
- all of uBlock Origin's stored data (tens of MB, mostly cached filter lists), so it starts with **default settings** after a restore; custom filters, allowlist and selected lists are reset

It also compacts SQLite databases larger than 10 MB (such as `places.sqlite`). A temporary copy of each one, including its `-wal` file, is run through `VACUUM` and that copy is archived instead of the original. Nothing is lost, and the profile itself is never modified. If a database cannot be vacuumed, the original is archived as is.

Still kept:

- bookmarks, history, passwords and cookies, so you stay logged in to most sites
- extensions and their storage (except uBlock Origin's, see above)
- prefs and `chrome/` customisations
- the current session (`zen-sessions.jsonlz4`, `sessionstore-backups/recovery.jsonlz4`), so your spaces, pinned tabs and open tabs come back

Sites that keep data locally, such as offline web apps or some chat and mail clients, may need to re-sync and can lose local-only state.

`--exclude-sensitive` makes a backup that's safer to share or store somewhere untrusted. It skips:

- saved passwords and the keys that decrypt them (`logins.json`, `logins.db`, `key4.db`)
- cookies (`cookies.sqlite`)
- saved form and search entries (`formhistory.sqlite`)
- saved addresses and cards (`autofill-profiles.json`)
- Firefox account tokens and Sync state (`signedInUser.json`, `weave/`, `synced-tabs.db`)
- website storage (localStorage/IndexedDB, which often hold login tokens)

You'll need to log in to sites again after restoring. Some sensitive data is still included:

- history and bookmarks
- open tabs and session files, which can contain session cookies and typed form text
- extension storage, which includes password-manager extension vaults

## What a restore does

1. Extracts the profile to a staging directory next to the target, so a failed restore leaves nothing half-written.
2. Rewrites absolute paths in `prefs.js` and `*.json` (e.g. extension paths in `extensions.json`) to the new profile location and home directory. This also works across machines, usernames and operating systems.
3. Drops `compatibility.ini` and `addonStartup.json.lz4`, so Zen rebuilds its caches for the new location.
4. When overwriting, moves the old profile to `<profile>.pre-restore-<timestamp>`. Disable this with `--no-keep-old`.
5. With `--new`, creates the profile directory and registers it in `profiles.ini`.

Each archive contains a `manifest.json` (source OS, paths, Zen version, size) plus `profile/`.

## Releasing

Releases are built by [GoReleaser](https://goreleaser.com) (`.goreleaser.yml`) when a tag is pushed:

1. Run the **Create tag** workflow (or `git tag vX.Y.Z && git push --tags`).
2. The **Release** workflow runs `make release`. This publishes the GitHub release, the deb/rpm/apk packages, the Homebrew cask (`kishaningithub/homebrew-tap`), the Scoop manifest (`kishaningithub/scoop-bucket`) and multi-arch images to `ghcr.io/kishaningithub/zen-backup-restore`.

The `GA_GORELEASER_TOKEN` secret must be a token with write access to this repo, the tap and the bucket. pkgx builds from source via its [pantry](https://github.com/pkgxdev/pantry), which picks up new GitHub releases automatically once the package is listed there.

Run `make build` locally to tidy, compile, test, lint and validate the GoReleaser config.
