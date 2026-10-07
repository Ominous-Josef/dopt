# dopt (Directory Optional Package Manager)

A manifest-driven package engine written in **Go** that installs, updates and desktop-integrates standalone Linux apps (tarballs, zips, AppImages and single binaries) cleanly into an `opt` folder.

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

---

## Why dopt?

Many good Linux applications and developer toolchains (Discord, Blender, Go, Neovim, JetBrains Toolbox, …) ship as standalone archives (`.tar.gz`, `.tar.xz`, `.zip`, AppImages or a single binary) that no package manager tracks. Managing them by hand is a repetitive chore:
* Unpacking them into random directories.
* Chasing down nested executable paths.
* Writing `.desktop` launchers so they appear in your app grid.
* Ending up with stale files or a broken app when an update is copied over the old folder.

**dopt** handles the whole cycle from a small JSON recipe (or a few interactive questions): it downloads or picks up the archive, installs it into its own folder, links the command into your `PATH` and creates the menu shortcut. Updates replace the app in one step, and a failed update leaves the previous version in place. dopt works alongside your system package manager, not instead of it.

---

## Key features

* **No root needed.** Apps install into `~/.local/opt` by default. `--global` installs system-wide into `/opt` with `sudo`.
* **Safe updates.** The new version is staged and checked next to the old one, then swapped in with two renames. Any failure, including Ctrl-C, restores the previous version.
* **Never touches what it didn't install.**
  - dopt keeps a registry of its installs.
  - Folders owned by an RPM or deb package are refused.
  - Unknown folders and command names that already exist are never overwritten without asking.
* **Many formats.** `.tar.gz`, `.tar.xz`, `.tar.bz2`, `.tar.zst`, `.zip`, AppImages and single Linux binaries (optionally compressed). The format is detected from the file itself, so oddly named downloads still work.
* **Update checks.** `dopt outdated` checks every installed app against its source without downloading anything, and `dopt update --all` installs the new releases.
* **Smart download URLs.** A recipe can find the latest release itself, from GitHub Releases or any JSON API (using jq queries), for x86_64 and aarch64.
* **Safe extraction.** Archive entries can't write outside the install folder, setuid bits are dropped, and hard links and symlinks are handled.
* **Running apps.** Running instances are found and stopped before an update, then relaunched as you, with your Wayland or X11 session.
* **Desktop integration.** dopt finds the app's icon and writes a validated `.desktop` launcher. CLI tools get none.
* **Checksums.** Downloads are verified against the SHA-256 their source publishes (GitHub's asset digest, a sums file, or the vendor's API). `--sha256` lets you supply one yourself.
* **Several commands per app.** A toolchain can link all its tools (`go` and `gofmt`), not just one.
* **Interactive mode.** No recipe? dopt asks for the details, pre-filled from the existing install when you update.
* **Manage what you installed.** `dopt list` and `dopt remove`.
* **Single static binary.** It doesn't need `curl`, `tar`, `xz`, `unzip` or `jq`.

---

## Installation

Requires Go 1.25 or newer.

```bash
git clone https://github.com/Ominous-Josef/dopt.git
cd dopt
go build -o dopt .
```

Then move `dopt` somewhere on your `PATH`, for example `~/.local/bin`.

## Usage

```text
dopt install [options]         Install or update an app (same App ID = update)
dopt list [-g]                 List installed apps
dopt outdated [-g] [<id>...]   Check installed apps for new releases
dopt update [-g] [-i] [-k] (--all | <id>...)
                               Install new releases (-k keeps the downloads)
dopt remove [-g] [-i] <id>     Uninstall an app installed by dopt
dopt version                   Show the dopt version
dopt help                      Show help
```

### Install options

```text
  -m, --manifest <json>   The application manifest (recipe)
  -a, --app-id <id>       App ID, when not using a manifest
  -s, --symlink-as <name> Command name to link (overrides the manifest's 'symlink_as')

  Source (choose one; if omitted, dopt offers your recent downloads):
  -d, --download          Download using the manifest's source
  -u, --url <url>         Download from this URL instead
  -f, --file <path>       Install from a local archive
      --sha256 <hash>     Verify the archive's SHA-256 checksum before installing

  -g, --global            Install system-wide to /opt (requires sudo)
  -c, --cleanup           Delete the downloaded or local archive after a successful setup
  -i, --install           Skip confirmation prompts; stop and relaunch a running app
```

The dopt 2.x form without a subcommand (`dopt -m recipe.json -d`) still works and means `install`.

**`-i` never decides for you.** It skips yes/no confirmations, and it stops and relaunches a running app. It aborts in these cases, because each needs a real choice:
- replacing a folder dopt didn't install
- a command-name clash
- choosing between a system-wide and a local copy
- no archive given

### Examples

1. **Install or update Go** from the latest release, deleting the download afterwards:
   ```bash
   dopt install -m recipes/golang.json -d -c
   ```

2. **Install system-wide** for all users:
   ```bash
   sudo dopt install -g -m recipes/neovim.json -d
   ```

3. **Install from a specific URL**, verifying its checksum:
   ```bash
   dopt install -m recipes/discord.json -u "https://discord.com/api/download?platform=linux&format=tar.gz" --sha256 <hash>
   ```

4. **No recipe:** point dopt at an archive and answer a few questions:
   ```bash
   dopt install -f ~/Downloads/some-app-linux-x64.tar.gz
   ```

5. **Pick from your recent downloads:** run `dopt install -m recipe.json` with no source. dopt lists the newest archives in your Downloads folder.

6. **Update everything:**
   ```bash
   dopt outdated
   dopt update --all
   ```

7. **Uninstall:**
   ```bash
   dopt list
   dopt remove neovim
   ```

---

## Keeping apps up to date

Every install saves the recipe it used (`<opt dir>/.dopt/recipes/<app_id>.json`) and records which release it downloaded. A download from `-u <url>` is saved as the recipe's fixed URL, so it can be checked later too.

```console
$ dopt outdated
  APP ID       INSTALLED   AVAILABLE   STATUS
  jq           jq-1.8.2    jq-1.8.2    up to date
  neovim       v0.12.4     v0.12.5     update available
  shellcheck   v0.11.0     v0.11.0     up to date
```

`dopt outdated` resolves each app's download again (GitHub release, API query or fixed URL) and asks the server what it serves now, without downloading it. A release is recognized by a version in its URL, including after redirects (for example Discord's fixed link redirects to a versioned file). When there's no version, dopt uses the server's `ETag`, or else `Last-Modified` and the size.

`dopt update --all` installs every available update through the normal install steps, so each one is staged, swapped and rolled back on failure. It asks once before starting (skip with `-i`) and doesn't keep the downloads unless you pass `-k`. Use `dopt update <app_id>` to update specific apps. Named apps are updated even when dopt can't tell whether they're current.

Some apps show "can't update":
- **Installed from a local file:** there's no download source to check.
- **Installed before dopt 3, or by dopt-bash:** there's no saved recipe. Install it once more with `-m <recipe>` or `-u <url>`, and it becomes updatable.

## Where things go

| | Local (default) | Global (`-g`, with sudo) |
|---|---|---|
| App files | `~/.local/opt/<app_id>/` | `/opt/<app_id>/` |
| Command link | `~/.local/bin/<symlink_as>` | `/usr/local/bin/<symlink_as>` |
| Menu shortcut | `~/.local/share/applications/<app_id>.desktop` | `/usr/share/applications/<app_id>.desktop` |
| Registry | `~/.local/opt/.dopt/<app_id>` | `/opt/.dopt/<app_id>` |
| Saved recipe | `~/.local/opt/.dopt/recipes/<app_id>.json` | `/opt/.dopt/recipes/<app_id>.json` |

If `~/.local/bin` isn't on your `PATH`, the summary after an install tells you.

**Updating is the same command as installing: same App ID = update.** dopt only ever installs into, and replaces, `<opt dir>/<app_id>`. If a local install would hide an existing system-wide copy of the same app, dopt offers three options:
- update the system-wide copy (it re-runs itself with `sudo`)
- install a separate `<app_id>-local` copy
- abort

### Why `/opt`?

Keeping each unmanaged app self-contained in its own folder gives two advantages:
- **Clean updates and uninstalls.** dopt can replace or delete `<opt dir>/<app_id>` without touching shared libraries or other tools in `/usr/local`.
- **Predictable layout.** Everything lives in one place, and the app is reachable through a single command link.

Modern binaries work out their real home from the link, so they run without extra environment variables.

### Ownership and the registry

dopt records each install in a small `key=value` file in `<opt dir>/.dopt/`. The entry stores the folder's identity (inode and creation time), so a folder that was deleted and recreated by something else no longer counts as dopt's.
- **Registered folders** are updated without asking.
- **Folders owned by a system package** (checked with `rpm -qf` / `dpkg -S`) are never touched. dopt names the package instead.
- **Anything else** gets a one-time "Replace it?" prompt that shows the folder's size and contents.

The registry format is shared with [dopt-bash](https://github.com/Ominous-Josef/dopt-bash), so either tool can update apps the other installed.

If an install fails or you cancel it after a download, dopt keeps the downloaded archive in the current folder (never overwriting a file) and prints the command to resume without downloading again.

---

## Writing a recipe (`recipes/`)

A recipe is a JSON file. A recipe for a CLI toolchain that finds its latest version through an API (`recipes/golang.json`):

```json
{
  "schema": 2,
  "app_id": "golang",
  "name": "Go Programming Language",
  "comment": "The Go programming language compiler and tools",
  "binary_path": "bin/go",
  "symlink_as": "go",
  "cli_only": true,
  "categories": "Development;",
  "source": {
    "type": "api",
    "endpoint": "https://go.dev/dl/?mode=json",
    "jq_query": ".[0].files[] | select(.os == $os and .arch == $goarch and .kind == \"archive\") | \"https://go.dev/dl/\\(.filename)\""
  }
}
```

### Fields

| Field | Type | Description |
|---|---|---|
| `schema` | Number | Manifest version; `2` is current. Files without it are read as version 1, as are dopt-bash manifests. |
| `app_id` | String | **Required.** Unique identifier, used as the install folder and launcher name. Letters, digits, `.`, `_` and `-` only. |
| `name` | String | Display name. Defaults to `app_id`. |
| `comment` | String | Short description for the launcher. |
| `binary_path` | String | Exact path to the executable, relative to the app folder. If the archive has one top-level folder, paths are relative to that folder. Takes priority over `binary_pattern`. |
| `binary_pattern` | String | File name or glob of the executable (case-insensitive), searched up to 3 levels deep; the shallowest match wins. One of `binary_path` and `binary_pattern` is required. For an AppImage or single-binary download, a plain name here (or in `binary_path`) becomes the installed file's name. |
| `icon_path` | String | Icon path relative to the app folder, or a file name to search for. Without it, dopt looks for a fitting icon. |
| `cli_only` | Boolean | `true` for terminal apps: no launcher is created. The string `"true"` is also accepted. |
| `symlink_as` | String | Command name to link. Defaults to `app_id`. Can be overridden with `-s`. |
| `categories` | String | Launcher categories, for example `Development;IDE;`. Defaults to `Utility;`. |
| `exec_flags` | String | Arguments added to the launcher's command, for example `--no-sandbox %U`. |
| `default_url_x64` | String | Fixed download URL for x86_64. |
| `default_url_arm64` | String | Fixed download URL for aarch64. |
| `source` | Object | Finds the download URL dynamically (see below). Used instead of `default_url_*`. |
| `binaries` | List | Extra commands to link, each `{"path": "bin/gofmt"}` or `{"pattern": "tool-*", "link_as": "tool"}`. `link_as` defaults to the file name. |

Unknown fields produce a warning, so typos don't go unnoticed. `default_install_dir` from version 1 recipes is ignored, because apps always install into `<opt dir>/<app_id>`.

### Sources

**`api`** fetches a JSON document and runs a jq query on it (gojq, no `jq` needed). The query can use these variables:
- `$arch`: `x64` or `arm64`
- `$goarch`: `amd64` or `arm64`
- `$os`: `linux`

The first string result is the URL.

```json
"source": {
  "type": "api",
  "endpoint": "https://data.services.jetbrains.com/products/releases?code=TBA&latest=true&type=release",
  "jq_query": ".TBA[0].downloads | if $arch == \"arm64\" then .linuxARM64 else .linux end | .link"
}
```

**`github`** and **`gitlab`** take an asset from the repository's latest release. GitLab sources may set `host` for a self-hosted instance (default `https://gitlab.com`).

```json
"source": {
  "type": "github",
  "repository": "neovim/neovim",
  "asset_pattern_x64": "nvim-linux-x86_64.tar.gz",
  "asset_pattern_arm64": "nvim-linux-arm64.tar.gz"
}
```

How the asset is chosen:
- **Patterns** can be substrings or globs. With a pattern, any matching asset can be picked, including binaries with no extension (`jq-linux-amd64`). Checksum and signature files are skipped.
- **`asset_pattern`** applies to both architectures.
- **No pattern:** dopt picks a Linux download whose name mentions the architecture, preferring `.tar.gz`, then the other archive types.

```json
"source": {
  "type": "gitlab",
  "repository": "gitlab-org/cli",
  "asset_pattern_x64": "linux_amd64.tar.gz",
  "asset_pattern_arm64": "linux_arm64.tar.gz",
  "checksums": "checksums.txt"
}
```

### Checksums

Unless you pass `--sha256`, dopt verifies a download against the SHA-256 its source publishes. It uses the first one it finds:

1. **`checksums`** (github/gitlab): a release asset listing SHA-256 sums, named in the recipe.
2. **GitHub's asset digest**, which GitHub publishes for every release asset.
3. **A sidecar file** named `<asset>.sha256` or `<asset>.sha256sum`.
4. **A sums file** with a common name: `SHA256SUMS`, `sha256sum.txt`, `checksums.txt`, `*_checksums.txt`, …
5. **`checksum_query`** (api): a jq query on the same JSON that returns either the hash or the URL of a checksum file.

```json
"checksum_query": ".[0].files[] | select(.os == $os and .arch == $goarch and .kind == \"archive\") | .sha256"
```

Sums files can be in GNU (`<hash>  <file>`), BSD (`SHA256 (<file>) = <hash>`) or single-hash format.
- **Checksums dopt finds on its own** (2–4) are best-effort: if one can't be read or doesn't list the download, dopt warns and continues.
- **A checksum the recipe asks for** (1 and 5) must verify, or nothing is installed.
- **`"checksums": "none"`** turns checking off.

A mismatch always stops the install, and the download is discarded.

---

## Development

```bash
go vet ./...
go test ./...                          # unit tests (no network, no root, nothing outside temp folders)
go test -tags e2e ./e2e/               # builds dopt and runs it end to end in a sandbox
go test -tags live ./internal/source   # resolves every recipe against the real vendor endpoints
```

Setting `DOPT_TEST_ROOT=<folder>` makes local mode write everything under that folder instead of your home. The end-to-end tests use it, and so can you when trying out changes:

```bash
DOPT_TEST_ROOT=$(mktemp -d) go run . install -m recipes/neovim.json -d
```

### Code layout

| Package | Role |
|---|---|
| `internal/cli` | Subcommands, flags, the setup wizard, the source picker |
| `internal/install` | The install/update pipeline, update checks, `remove` and `list` |
| `internal/manifest` | Recipe schema, defaults and validation |
| `internal/source` | URL resolution (api/github), downloads, release fingerprints, checksums, keeping archives |
| `internal/archive` | Format detection and safe extraction (tar with any supported compression, zip, single files) |
| `internal/registry` | The `.dopt` install registry |
| `internal/layout` | Local, global and test-mode folders |
| `internal/linker` | Command links and PATH checks |
| `internal/proc` | Finding, stopping and relaunching the app |
| `internal/desktop` | Launchers and icon search |
| `internal/owner`, `binfind`, `names`, `sysuser`, `ui` | Package ownership, finding binaries, name rules, the real user, output and prompts |

---

## Limitations

- **AppImages** are installed as they are. They need FUSE (`libfuse2`) to run, and dopt doesn't extract their icon, so the menu shortcut uses a generic one unless you set `icon_path`.
- **Update checks** only work when the server says which release it serves. A fixed URL without a version, `ETag` or `Last-Modified` shows as "unknown"; `dopt update <app_id>` still reinstalls it.
- **Installers** (`.run`, `.sh`, `.deb`, `.rpm`) aren't supported.
- **Platform:** Linux on x86_64 or aarch64.
- **Package detection:** only RPM and dpkg are checked. On other systems, an unregistered folder still gets the "Replace it?" prompt.

## Disclaimer & security responsibility

> [!CAUTION]
> **No signature verification:** dopt installs and runs software exactly as provided. It checks SHA-256 checksums (published by the source, or supplied by you), which catch corrupted or swapped downloads, but it does not check signatures. A checksum served from the same place as the download can't protect against that place being compromised.
>
> In local mode dopt runs as you; with `--global` it runs as root. Either way:
> - Only use URLs, recipes and archives you trust.
> - You are responsible for verifying any recipe or archive you download.

---

## License

Distributed under the GNU General Public License v3 (GPLv3). See `LICENSE` for more information.
