# dopt (Directory Optional Package Manager)

A lightweight, manifest-driven package engine built natively in **Go** to deploy, update, and desktop-integrate standalone Linux tarballs cleanly into `/opt`.

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)

---

## Why dopt?

Many incredible Linux applications and developer toolchains (like Discord, Blender, Go, and Neovim) are distributed as standalone compressed `.tar.gz` archives. Managing these manually is a repetitive, messy chore:
* Unpacking them into random directories.
* Chasing down nested executable paths.
* Manually writing `.desktop` launcher files so they appear in your desktop application grid.
* Running into ghost-file or dependency corruption issues during updates because old folders weren't wiped cleanly.

**dopt** fixes this by introducing a **stateless, recipe-driven lifecycle**. You define how an app behaves in a simple JSON recipe (or use the interactive CLI), and the core engine handles the downloading, extracting, linking, process management, and desktop integration safely and atomically.

---

## Key Features

* **Zero External Dependencies:** Native modular Go implementation for downloading (`net/http`), extracting (`archive/tar`, `compress/gzip`), and parsing JSON (`gojq`). No need to install `curl`, `tar`, or `jq`.
* **Smart Agnostic URLs:** Recipes dynamically query vendor endpoints (like GitHub Releases or custom JSON APIs using `jq` queries) to resolve the latest download URL based on your system architecture.
* **Strict Directory Isolation:** Installs applications entirely inside their own sandbox under `/opt/[app-id]/`.
* **Active Process Management:** Automatically detects if an application is currently running, suspends it during the update cycle, and safely relaunches it (with `WAYLAND_DISPLAY` support).
* **Dynamic Desktop Integration:** Automatically scavenges the source archive for application icons and injects standard-compliant launchers into `/usr/share/applications/`.
* **Interactive Mode:** Allows you to install an application directly without needing a manifest by using an interactive prompt.
* **CLI & Headless Support:** Supports an explicit `cli_only` flag to bypass GUI shortcut creation for terminal toolchains.

---

## Installation

Clone the repository and compile the native Go binary:

```bash
git clone https://github.com/Ominous-Josef/dopt.git
cd dopt
go mod tidy
go build -o dopt main.go
```

## Usage & Command-Line Flags

Modifications to the system application layer require root privileges. Always invoke `dopt` using `sudo`.

```text
Usage: sudo ./dopt [options]

Core Options:
  -m, --manifest <json>   The application manifest recipe configuration file
  -a, --app-id <id>       Provide App ID directly for interactive setup (if no manifest is used)

Deployment Targets (Choose one):
  -d, --download          Download using the manifest's smart source endpoint
  -u, --url <url>         Download using a specific direct link override
  -f, --file <path>       Directly deploy from a local archive package file
  -p, --path <dir>        Scan a specific directory folder for a matching local archive

Modifiers:
  -c, --cleanup           Delete downloaded installer archive after a successful setup
  -i, --install           Force run a fresh setup without checking interactive prompts
  -h, --help              Show this help menu
```

### Examples

1. **Install/Update Go** natively using its recipe's default API URL:
```bash
sudo ./dopt -m recipes/golang.json --download --cleanup
```

2. **Deploy an app using a direct URL override** (bypassing the recipe's default):
```bash
sudo ./dopt -m recipes/discord.json --url "https://discord.com/api/download?platform=linux&format=tar.gz"
```

---

## Interactive Installation (No Manifest)

If you don't have a `.json` recipe for an application, you can install it directly by passing a file or URL. `dopt` will interactively prompt you for the necessary metadata (App ID, Name, Symlink Name, etc.) to generate the integration layout on the fly.

1. **Install from a local file:**
```bash
sudo ./dopt --file ~/Downloads/custom-app-linux.tar.gz
```
*(You will be prompted for the App ID and other details in the terminal).*

2. **Bypass the first prompt by providing an App ID upfront:**
```bash
sudo ./dopt --app-id custom-app --file ~/Downloads/custom-app-linux.tar.gz
```

3. **Install directly from a remote URL:**
```bash
sudo ./dopt --app-id my-tool --url "https://example.com/downloads/my-tool.tar.gz"
```

---

## Writing a Recipe (`recipes/`)

Applications are registered via simple JSON blueprints. Here is an example manifest for a CLI tool chain that uses a dynamic API query to resolve the latest version (`recipes/golang.json`):

```json
{
  "app_id": "golang",
  "name": "Go Programming Language",
  "comment": "The Go programming language compiler and tools",
  "default_install_dir": "/opt/go",
  "binary_pattern": "go",
  "binary_path": "bin/go",
  "symlink_as": "go",
  "cli_only": true,
  "categories": "Development;",
  "exec_flags": "",
  "source": {
    "type": "api",
    "endpoint": "https://go.dev/dl/?mode=json",
    "jq_query": ".[0].files[] | select(.os==\"linux\" and .arch==\"amd64\") | \"https://go.dev/dl/\\(.filename)\""
  }
}
```

For applications distributed on GitHub, you can use the `github` source type:

```json
  "source": {
    "type": "github",
    "repository": "neovim/neovim",
    "asset_pattern": "linux64"
  }
```

---

## System Layout Bindings

When an archive is parsed, `dopt` structures its contents across standard Linux system tracks:

* **Application Sandbox:** `/opt/[app-id]/`
* **Global Executable Link:** `/usr/local/bin/[symlink_as]`
* **System Desktop Shortcuts:** `/usr/share/applications/[app-id].desktop`

---

## Why `/opt`? (The Golden Rule of Isolation)

According to the Linux Filesystem Hierarchy Standard (FHS), third-party software and developer toolchains are often placed in `/usr/local`. However, `dopt` enforces a strict rule: **Keep every single unmanaged application completely self-contained in its own sandbox under `/opt`.**

Using `/opt/[app-id]` gives you massive architectural advantages:

* **Atomic Updates & Clean Uninstalls:** When you run an update, `dopt` can confidently wipe `/opt/[app-id]` knowing it won't accidentally cross wires with shared libraries or other system tools housed in `/usr/local`.
* **Predictable Architecture:** Everything lives predictably under `/opt/`, and we simply expose the executable to your system `PATH` via a single clean symlink in `/usr/local/bin/`.

**Will moving applications away from `/usr/local` break them?**
No. Modern binaries are smart enough to automatically deduce their true home directories dynamically (even when invoked via a symlink in `/usr/local/bin/`), allowing them to function flawlessly without forcing you to manually configure environment variables or update your system `PATH`.

---

## Code Architecture

The `dopt` engine is designed natively in Go and structured across highly modular internal packages:
- `internal/manifest`: Core data structures and recipe logic.
- `internal/network`: API-driven download orchestration (`gojq` enabled).
- `internal/archive`: High-speed decompression for `.tar.gz`.
- `internal/desktop`: System framework integration and file generation.

---

## Disclaimer & Security Responsibility

> [!CAUTION]
> **No Cryptographic Verification:** `dopt` is a deployment engine. It **does not** cryptographically verify signatures or the safety of the payloads it installs. 
> 
> Because `dopt` runs with `sudo` privileges to install system-wide applications:
> - You must 100% trust the source `URL` you provide.
> - You are responsible for verifying the integrity of any `manifest.json` file you download from the internet.
> - You are responsible for verifying the integrity of local `.tar.gz` archives before passing them to `dopt`.

---

## License

Distributed under the GNU General Public License v3 (GPLv3). See `LICENSE` for more information. A copyleft license that ensures `dopt` will stay free, transparent, and open-source forever.
