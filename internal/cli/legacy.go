package cli

// Temporary: the pre-3.0 install flow, kept so `dopt install` works while the
// new pipeline (internal/install) is built. Replaced in the install-pipeline phase.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Ominous-Josef/dopt/internal/archive"
	"github.com/Ominous-Josef/dopt/internal/desktop"
	"github.com/Ominous-Josef/dopt/internal/manifest"
	"github.com/Ominous-Josef/dopt/internal/names"
	"github.com/Ominous-Josef/dopt/internal/source"
	"github.com/Ominous-Josef/dopt/internal/sysuser"
	"github.com/Ominous-Josef/dopt/internal/ui"
	"github.com/Ominous-Josef/dopt/internal/utils"
)

var binLinkDir = "/usr/local/bin"

func runInstall(o InstallOptions) error {
	if o.Global || o.Symlink != "" || o.SHA256 != "" {
		return errors.New("-g, -s and --sha256 aren't available yet in this development build")
	}
	if os.Geteuid() != 0 {
		return errors.New("this development build still installs system-wide only. Re-run with sudo")
	}
	usr, err := sysuser.Real()
	if err != nil {
		return err
	}
	u := ui.Std()

	var m manifest.Manifest
	if o.Manifest != "" {
		var warnings []string
		m, warnings, err = manifest.Load(o.Manifest)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			u.Warn("%s", w)
		}
	} else {
		u.Heading("Interactive setup")
		m.AppID = o.AppID
		if m.AppID == "" {
			if m.AppID, err = u.Ask("Enter App ID (e.g. com.example.app): "); err != nil {
				return err
			}
		}
		if err := names.Validate("App ID", m.AppID); err != nil {
			return err
		}
		if m.Name, err = u.AskDefault(m.AppID, "Enter Application Name [%s]: ", m.AppID); err != nil {
			return err
		}
		if m.SymlinkAs, err = u.AskDefault(m.AppID, "Enter executable symlink name [%s]: ", m.AppID); err != nil {
			return err
		}
		cli, err := u.Confirm(false, "Is this a CLI-only application? [y/N]: ")
		if err != nil {
			return err
		}
		m.CliOnly = manifest.FlexBool(cli)
		m.BinaryPattern = m.SymlinkAs
		m.ApplyDefaults()
		if err := m.Validate(); err != nil {
			return err
		}
	}

	fmt.Printf("[*] Auditing environment path structures for %s...\n", m.Name)
	binLink := filepath.Join(binLinkDir, m.SymlinkAs)
	installDir := filepath.Join("/opt", m.AppID)

	if _, err := os.Lstat(binLink); err != nil && !o.Force {
		ok, err := u.Confirm(true, "No version found. Perform a clean installation of %s at %s? [Y/n]: ", m.Name, installDir)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("[-] Deployment aborted.")
			return nil
		}
	}

	var downloadUrl string
	if o.URL != "" {
		downloadUrl = o.URL
	} else if o.Download {
		arch, err := manifest.HostArch()
		if err != nil {
			return err
		}
		if downloadUrl, err = source.Resolve(context.Background(), m, arch); err != nil {
			return err
		}
		fmt.Printf("[+] Resolved download URL: %s\n", downloadUrl)
	}
	if o.Download && downloadUrl == "" {
		return errors.New("no download URL found. Use -u <url>")
	}

	var archivePath string
	if o.File != "" {
		archivePath = o.File
		if strings.HasPrefix(archivePath, "~") {
			archivePath = strings.Replace(archivePath, "~", usr.Home, 1)
		}
		if !utils.FileExists(archivePath) {
			return fmt.Errorf("archive not found: %s", archivePath)
		}
	} else if downloadUrl != "" {
		dlDir, err := os.MkdirTemp("", "dopt-download-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dlDir)
		archivePath = filepath.Join(dlDir, "source_package.tar.gz")
		if err := source.Download(context.Background(), downloadUrl, archivePath, nil); err != nil {
			return err
		}
	} else {
		return errors.New("no archive given. Pass -f <file>, -u <url> or -d")
	}

	tmpDir, err := os.MkdirTemp("", "dopt-workspace-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	fmt.Printf("[*] Extracting execution code assets...\n")
	if err := archive.Extract(archivePath, tmpDir); err != nil {
		return fmt.Errorf("extracting archive: %w", err)
	}
	if tmpDir, err = archive.InstallRoot(tmpDir); err != nil {
		return err
	}

	var localBin string
	if m.BinaryPath != "" {
		localBin = filepath.Join(tmpDir, m.BinaryPath)
	} else {
		localBin = utils.FindBinary(tmpDir, m.BinaryPattern)
	}

	restartReqd := false
	if localBin != "" && utils.FileExists(localBin) {
		escapedBinName := regexp.QuoteMeta(filepath.Base(localBin))
		if exec.Command("pgrep", "-u", usr.Name, "-f", escapedBinName).Run() == nil {
			ok, err := u.Confirm(true, "%s is currently running. Kill process, deploy, and auto-restart? [Y/n]: ", m.Name)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("[-] Update cycle canceled to keep app active.")
				return nil
			}
			exec.Command("pkill", "-u", usr.Name, "-f", escapedBinName).Run()
			time.Sleep(1500 * time.Millisecond)
			exec.Command("pkill", "-9", "-u", usr.Name, "-f", escapedBinName).Run()
			restartReqd = !bool(m.CliOnly)
		}
	}

	fmt.Println("[*] Deep cleaning legacy directory mappings to clear stale libraries...")
	os.RemoveAll(installDir)
	os.MkdirAll(installDir, 0755)

	fmt.Println("[*] Synchronizing updated frameworks into installation path...")
	if err := utils.CopyDir(tmpDir, installDir); err != nil {
		return fmt.Errorf("copying files: %w", err)
	}

	var binaryTarget string
	if m.BinaryPath != "" {
		binaryTarget = filepath.Join(installDir, m.BinaryPath)
	} else {
		binaryTarget = utils.FindBinary(installDir, m.BinaryPattern)
	}
	if binaryTarget == "" || !utils.FileExists(binaryTarget) {
		return errors.New("the binary wasn't found inside the installation")
	}

	os.Chmod(binaryTarget, 0755)
	os.Remove(binLink)
	if err := os.Symlink(binaryTarget, binLink); err != nil {
		return fmt.Errorf("creating symlink: %w", err)
	}
	fmt.Printf("[+] Symlink created: %s -> %s\n", binLink, binaryTarget)

	if !m.CliOnly {
		err := desktop.Write(filepath.Join("/usr/share/applications", m.AppID+".desktop"), desktop.Entry{
			Name: m.Name, Comment: m.Comment, Command: binLink, Flags: m.ExecFlags,
			Icon:       desktop.FindIcon(installDir, m.IconPath, m.AppID, m.SymlinkAs),
			Categories: m.Categories, WMClass: filepath.Base(binaryTarget),
		})
		if err != nil {
			return err
		}
	}

	if downloadUrl != "" {
		if o.Cleanup {
			os.Remove(archivePath)
		} else {
			cwd, _ := os.Getwd()
			outputDest, err := source.Keep(archivePath, downloadUrl, m.AppID, cwd, usr.UID, usr.GID)
			if err != nil {
				return err
			}
			fmt.Printf("[i] Local installation backup kept at: %s\n", outputDest)
		}
	}

	if restartReqd {
		cmd := exec.Command("sudo", "-u", usr.Name, "nohup", binLink)
		cmd.Env = append(os.Environ(), fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", usr.UID))
		cmd.Start()
	}

	fmt.Printf("\n[+] Success! %s has been deployed via dopt.\n", m.Name)
	return nil
}
