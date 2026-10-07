# dopt (Directory Optional Package Manager)

A manifest-driven package engine written in **Go** that installs, updates and desktop-integrates standalone Linux app archives cleanly into an `opt` folder.

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

---

## Why dopt?

Many good Linux applications and developer toolchains (Discord, Blender, Go, Neovim, JetBrains Toolbox, …) ship as standalone `.tar.gz` archives that no package manager tracks. Managing them by hand is a repetitive chore:
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
* **Smart download URLs.** A recipe can find the latest release itself, from GitHub Releases or any JSON API (using jq queries), for x86_64 and aarch64.
* **Safe extraction.** Archive entries can't write outside the install folder, setuid bits are dropped, and hard links and symlinks are handled.
* **Running apps.** Running instances are found and stopped before an update, then relaunched as you, with your Wayland or X11 session.
* **Desktop integration.** dopt finds the app's icon and writes a validated `.desktop` launcher. CLI tools get none.
* **Checksums.** `--sha256` verifies an archive before anything is installed.
* **Interactive mode.** No recipe? dopt asks for the details, pre-filled from the existing install when you update.
* **Manage what you installed.** `dopt list` and `dopt remove`.
* **Single static binary.** It doesn't need `curl`, `tar` or `jq`.

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
dopt install [options]      Install or update an app (same App ID = update)
dopt list [-g]              List installed apps
dopt remove [-g] [-i] <id>  Uninstall an app installed by dopt
dopt version                Show the dopt version
dopt help                   Show help
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

6. **Uninstall:**
   ```bash
   dopt list
   dopt remove neovim
   ```

---

## Where things go

| | Local (default) | Global (`-g`, with sudo) |
|---|---|---|
| App files | `~/.local/opt/<app_id>/` | `/opt/<app_id>/` |
| Command link | `~/.local/bin/<symlink_as>` | `/usr/local/bin/<symlink_as>` |
| Menu shortcut | `~/.local/share/applications/<app_id>.desktop` | `/usr/share/applications/<app_id>.desktop` |
| Registry | `~/.local/opt/.dopt/<app_id>` | `/opt/.dopt/<app_id>` |

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
| `binary_pattern` | String | File name or glob of the executable (case-insensitive), searched up to 3 levels deep; the shallowest match wins. One of `binary_path` and `binary_pattern` is required. |
| `icon_path` | String | Icon path relative to the app folder, or a file name to search for. Without it, dopt looks for a fitting icon. |
| `cli_only` | Boolean | `true` for terminal apps: no launcher is created. The string `"true"` is also accepted. |
| `symlink_as` | String | Command name to link. Defaults to `app_id`. Can be overridden with `-s`. |
| `categories` | String | Launcher categories, for example `Development;IDE;`. Defaults to `Utility;`. |
| `exec_flags` | String | Arguments added to the launcher's command, for example `--no-sandbox %U`. |
| `default_url_x64` | String | Fixed download URL for x86_64. |
| `default_url_arm64` | String | Fixed download URL for aarch64. |
| `source` | Object | Finds the download URL dynamically (see below). Used instead of `default_url_*`. |

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

**`github`** takes an asset from the repository's latest release:

```json
"source": {
  "type": "github",
  "repository": "neovim/neovim",
  "asset_pattern_x64": "nvim-linux-x86_64.tar.gz",
  "asset_pattern_arm64": "nvim-linux-arm64.tar.gz"
}
```

How the asset is chosen:
- **Patterns** can be substrings or globs.
- **`asset_pattern`** applies to both architectures.
- **No pattern:** dopt picks a Linux `.tar.gz` whose name mentions the architecture.

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
| `internal/install` | The install/update pipeline, `remove` and `list` |
| `internal/manifest` | Recipe schema, defaults and validation |
| `internal/source` | URL resolution (api/github), downloads, checksums, keeping archives |
| `internal/archive` | Safe extraction |
| `internal/registry` | The `.dopt` install registry |
| `internal/layout` | Local, global and test-mode folders |
| `internal/linker` | Command links and PATH checks |
| `internal/proc` | Finding, stopping and relaunching the app |
| `internal/desktop` | Launchers and icon search |
| `internal/owner`, `binfind`, `names`, `sysuser`, `ui` | Package ownership, finding binaries, name rules, the real user, output and prompts |

---

## Limitations

- **Archive format:** only `.tar.gz` / `.tgz` for now.
- **Platform:** Linux on x86_64 or aarch64.
- **Package detection:** only RPM and dpkg are checked. On other systems, an unregistered folder still gets the "Replace it?" prompt.

## Disclaimer & security responsibility

> [!CAUTION]
> **No signature verification:** dopt installs and runs software exactly as provided. It does not check signatures, only an optional SHA-256 checksum that you supply.
>
> In local mode dopt runs as you; with `--global` it runs as root. Either way:
> - Only use URLs, recipes and archives you trust.
> - You are responsible for verifying any recipe or archive you download.

---

## License

Distributed under the GNU General Public License v3 (GPLv3). See `LICENSE` for more information.
