package main

import (
	"encoding/json"
	"flag"
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
	"github.com/Ominous-Josef/dopt/internal/network"
	"github.com/Ominous-Josef/dopt/internal/utils"
)

var binLinkDir = "/usr/local/bin"

func main() {
	manifestPath := flag.String("m", "", "The application manifest recipe configuration file")
	flag.StringVar(manifestPath, "manifest", "", "The application manifest recipe configuration file")

	appIdCli := flag.String("a", "", "Provide App ID directly if not using a manifest")
	flag.StringVar(appIdCli, "app-id", "", "Provide App ID directly if not using a manifest")

	download := flag.Bool("d", false, "Download using the manifest's default server endpoint")
	flag.BoolVar(download, "download", false, "Download using the manifest's default server endpoint")

	cleanup := flag.Bool("c", false, "Delete downloaded installer archive after a successful setup")
	flag.BoolVar(cleanup, "cleanup", false, "Delete downloaded installer archive after a successful setup")

	forceInstall := flag.Bool("i", false, "Force run a fresh setup without checking prompts")
	flag.BoolVar(forceInstall, "install", false, "Force run a fresh setup without checking prompts")

	customUrl := flag.String("u", "", "Download using a specific direct link override")
	flag.StringVar(customUrl, "url", "", "Download using a specific direct link override")

	filePath := flag.String("f", "", "Directly deploy from a local archive package file")
	flag.StringVar(filePath, "file", "", "Directly deploy from a local archive package file")

	searchDir := flag.String("p", ".", "Scan a specific directory folder for a matching local archive")
	flag.StringVar(searchDir, "path", ".", "Scan a specific directory folder for a matching local archive")

	flag.Usage = func() {
		fmt.Println("dopt - Dynamic Optional Package Manager (Go Engine)")
		fmt.Println("Usage: sudo ./dopt -m <recipe.json> [options]")
		flag.PrintDefaults()
	}

	flag.Parse()

	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "[-] Error: dopt engine modifications require root context. Re-run command using sudo.")
		os.Exit(1)
	}

	realUser := os.Getenv("SUDO_USER")
	if realUser == "" {
		realUser = os.Getenv("USER")
	}
	userHome := ""
	if realUser != "" {
		out, err := exec.Command("sh", "-c", fmt.Sprintf("getent passwd %s | cut -d: -f6", realUser)).Output()
		if err == nil {
			userHome = strings.TrimSpace(string(out))
		}
	}

	var m manifest.Manifest
	if *manifestPath != "" {
		manifestData, err := os.ReadFile(*manifestPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] Error reading manifest: %v\n", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(manifestData, &m); err != nil {
			fmt.Fprintf(os.Stderr, "[-] Error parsing manifest: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("[*] No manifest provided. Using interactive setup...")
		m.AppID = *appIdCli
		if m.AppID == "" {
			fmt.Print("[?] Enter App ID (e.g. com.example.app): ")
			fmt.Scanln(&m.AppID)
		}
		if m.AppID == "" {
			fmt.Fprintln(os.Stderr, "[-] Error: App ID is required.")
			os.Exit(1)
		}

		fmt.Printf("[?] Enter Application Name [%s]: ", m.AppID)
		var appName string
		fmt.Scanln(&appName)
		if appName == "" {
			appName = m.AppID
		}
		m.Name = appName

		fmt.Printf("[?] Enter executable symlink name [%s]: ", m.AppID)
		var symlinkName string
		fmt.Scanln(&symlinkName)
		if symlinkName == "" {
			symlinkName = m.AppID
		}
		m.SymlinkAs = symlinkName

		fmt.Print("[?] Is this a CLI-only application? [y/N]: ")
		var cliAns string
		fmt.Scanln(&cliAns)
		cliAns = strings.ToLower(strings.TrimSpace(cliAns))
		if strings.HasPrefix(cliAns, "y") {
			m.CliOnly = true
		} else {
			m.CliOnly = false
		}

		m.DefaultInstallDir = "/opt/" + m.AppID
		m.Categories = "Utility;"
	}

	// Input Sanitization
	if strings.Contains(m.AppID, "/") || strings.Contains(m.AppID, "..") || strings.Contains(m.SymlinkAs, "/") || strings.Contains(m.SymlinkAs, "..") {
		fmt.Fprintln(os.Stderr, "[-] CRITICAL: Security abort. APP_ID and SYMLINK_NAME cannot contain path traversal characters (/, ..).")
		os.Exit(1)
	}

	fmt.Printf("[*] Auditing environment path structures for %s...\n", m.Name)
	binLink := filepath.Join(binLinkDir, m.SymlinkAs)
	installDir := m.DefaultInstallDir

	if _, err := os.Lstat(binLink); err == nil {
		target, err := filepath.EvalSymlinks(binLink)
		if err == nil {
			installDir = filepath.Dir(target)
			if m.BinaryPath != "" && m.BinaryPath != "null" {
				depth := strings.Count(m.BinaryPath, "/")
				for i := 0; i <= depth; i++ {
					installDir = filepath.Dir(installDir)
				}
			}
			fmt.Printf("[+] Map match: Found existing installation via symlink at %s\n", installDir)
		}
	} else if !*forceInstall {
		fmt.Printf("\n[?] No version found. Perform a clean installation of %s at %s? [Y/n]: ", m.Name, installDir)
		var response string
		fmt.Scanln(&response)
		response = strings.ToLower(strings.TrimSpace(response))
		if response == "n" || response == "no" {
			fmt.Println("[-] Deployment aborted.")
			os.Exit(0)
		}
	}

	var downloadUrl string
	if *customUrl != "" {
		downloadUrl = *customUrl
	} else if *download || *forceInstall {
		downloadUrl = network.ResolveDownloadUrl(m)
		if downloadUrl == "" && *manifestPath != "" {
			fmt.Println("[-] Warning: Failed to resolve dynamic download URL.")
		} else if downloadUrl != "" {
			fmt.Printf("[+] Resolved download URL: %s\n", downloadUrl)
		}
	}

	if *download && downloadUrl == "" {
		fmt.Fprintln(os.Stderr, "[-] Error: No download URL provided. Use -u <url> if not using a manifest.")
		os.Exit(1)
	}

	var archivePath string
	if *filePath != "" {
		archivePath = *filePath
		if strings.HasPrefix(archivePath, "~") && userHome != "" {
			archivePath = strings.Replace(archivePath, "~", userHome, 1)
		}
		if !utils.FileExists(archivePath) {
			fmt.Fprintf(os.Stderr, "[-] Path fault: Target file missing: %s\n", archivePath)
			os.Exit(1)
		}
	} else if downloadUrl != "" {
		archivePath = network.DownloadFile(downloadUrl, m.AppID)
	} else {
		searchDirPath := *searchDir
		if strings.HasPrefix(searchDirPath, "~") && userHome != "" {
			searchDirPath = strings.Replace(searchDirPath, "~", userHome, 1)
		}
		fmt.Printf("[*] Scanning directories under '%s' for updates...\n", searchDirPath)
		
		cmd := exec.Command("sh", "-c", fmt.Sprintf("ls -t %s/*%s*.tar.gz 2>/dev/null | head -n 1", searchDirPath, m.AppID))
		out, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			archivePath = strings.TrimSpace(string(out))
		} else {
			fmt.Fprintf(os.Stderr, "[-] Archive fault: No deployment packages matching *%s*.tar.gz found.\n", m.AppID)
			os.Exit(1)
		}
	}

	if archivePath == "" {
		fmt.Fprintln(os.Stderr, "[-] Error: No archive file provided or found.")
		os.Exit(1)
	}

	tmpDir, err := os.MkdirTemp("", "dopt-workspace-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error creating temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	fmt.Printf("[*] Extracting execution code assets...\n")
	if err := archive.ExtractTarGz(archivePath, tmpDir); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error extracting archive: %v\n", err)
		os.Exit(1)
	}

	var localBin string
	if m.BinaryPath != "" && m.BinaryPath != "null" {
		localBin = filepath.Join(tmpDir, m.BinaryPath)
	} else {
		localBin = utils.FindBinary(tmpDir, m.BinaryPattern)
	}

	restartReqd := false
	if localBin != "" && utils.FileExists(localBin) {
		runningBinName := filepath.Base(localBin)
		escapedBinName := regexp.QuoteMeta(runningBinName)
		pgrepCmd := exec.Command("pgrep", "-u", realUser, "-f", escapedBinName)
		if err := pgrepCmd.Run(); err == nil {
			fmt.Printf("\n[!] Active Process Block: %s is currently running.\n", m.Name)
			fmt.Print("[?] Kill process, deploy workspace matrix, and auto-restart? [Y/n]: ")
			var runRes string
			fmt.Scanln(&runRes)
			runRes = strings.ToLower(strings.TrimSpace(runRes))
			if runRes == "n" || runRes == "no" {
				fmt.Println("[-] Update cycle canceled to keep app active.")
				os.Exit(0)
			}
			exec.Command("pkill", "-u", realUser, "-f", escapedBinName).Run()
			time.Sleep(1500 * time.Millisecond)
			exec.Command("pkill", "-9", "-u", realUser, "-f", escapedBinName).Run()
			if !m.CliOnly {
				restartReqd = true
			}
		}
	}

	installDirAbs, _ := filepath.Abs(installDir)
	safeDirs := []string{"/", "/usr", "/bin", "/etc", "/var", "/opt", "/home", "/usr/local", "/usr/share", "/usr/local/bin"}
	for _, safeDir := range safeDirs {
		if installDirAbs == safeDir {
			fmt.Fprintf(os.Stderr, "[-] CRITICAL: Safety abort. Attempted to delete system directory: %s\n", installDirAbs)
			os.Exit(1)
		}
	}

	fmt.Println("[*] Deep cleaning legacy directory mappings to clear stale libraries...")
	os.RemoveAll(installDir)
	os.MkdirAll(installDir, 0755)

	fmt.Println("[*] Synchronizing updated frameworks into installation path...")
	if err := utils.CopyDir(tmpDir, installDir); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error copying files: %v\n", err)
		os.Exit(1)
	}

	var binaryTarget string
	if m.BinaryPath != "" && m.BinaryPath != "null" {
		binaryTarget = filepath.Join(installDir, m.BinaryPath)
	} else {
		binaryTarget = utils.FindBinary(installDir, m.BinaryPattern)
	}

	if binaryTarget == "" || !utils.FileExists(binaryTarget) {
		fmt.Fprintf(os.Stderr, "[-] Critical Error: Execution file vector verification failed inside installation target.\n")
		os.Exit(1)
	}

	os.Chmod(binaryTarget, 0755)
	os.Remove(binLink)
	if err := os.Symlink(binaryTarget, binLink); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error creating symlink: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[+] Symlink created: %s -> %s\n", binLink, binaryTarget)

	if !m.CliOnly {
		desktop.CreateDesktopEntry(m, installDir, binLink, binaryTarget)
	}

	if downloadUrl != "" {
		if *cleanup {
			fmt.Println("[*] Removing compressed remote runtime package artifacts...")
			os.Remove(archivePath)
		} else {
			urlFileName := filepath.Base(downloadUrl)
			urlFileName = strings.ReplaceAll(urlFileName, "%20", " ")
			if strings.HasPrefix(urlFileName, "download") || urlFileName == "" || urlFileName == "." || urlFileName == "/" {
				urlFileName = fmt.Sprintf("%s-linux.tar.gz", m.AppID)
			}
			cwd, _ := os.Getwd()
			outputDest := filepath.Join(cwd, urlFileName)
			
			if err := os.Rename(archivePath, outputDest); err != nil {
				utils.CopyFile(archivePath, outputDest)
				os.Remove(archivePath)
			}
			if realUser != "" {
				exec.Command("chown", fmt.Sprintf("%s:", realUser), outputDest).Run()
			}
			fmt.Printf("[i] Local installation backup kept at: %s\n", outputDest)
		}
	}

	if restartReqd {
		fmt.Println("[*] Relaunching application window environment inside active desktop framework layer...")
		cmdStr := fmt.Sprintf("nohup %s > /dev/null 2>&1 &", binLink)
		cmd := exec.Command("sudo", "-u", realUser, "bash", "-c", cmdStr)
		
		uidCmd := exec.Command("id", "-u", realUser)
		uidOut, _ := uidCmd.Output()
		uid := strings.TrimSpace(string(uidOut))
		
		env := os.Environ()
		display := os.Getenv("DISPLAY")
		if display == "" { display = ":0" }
		env = append(env, fmt.Sprintf("DISPLAY=%s", display))
		if wayland := os.Getenv("WAYLAND_DISPLAY"); wayland != "" {
			env = append(env, fmt.Sprintf("WAYLAND_DISPLAY=%s", wayland))
		}
		env = append(env, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%s", uid))
		cmd.Env = env
		
		cmd.Start()
		fmt.Println("[+] Application successfully brought back online.")
	}

	fmt.Printf("\n[+] Success! %s has been deployed via dopt.\n", m.Name)
}
