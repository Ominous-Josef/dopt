package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/itchyny/gojq"
)

type Source struct {
	Type         string `json:"type"`
	Endpoint     string `json:"endpoint,omitempty"`
	JqQuery      string `json:"jq_query,omitempty"`
	Repository   string `json:"repository,omitempty"`
	AssetPattern string `json:"asset_pattern,omitempty"`
}

type Manifest struct {
	AppID             string `json:"app_id"`
	Name              string `json:"name"`
	Comment           string `json:"comment"`
	DefaultInstallDir string `json:"default_install_dir"`
	BinaryPattern     string `json:"binary_pattern"`
	BinaryPath        string `json:"binary_path"`
	SymlinkAs         string `json:"symlink_as"`
	CliOnly           bool   `json:"cli_only"`
	Categories        string `json:"categories"`
	ExecFlags         string `json:"exec_flags"`
	DefaultUrlX64     string `json:"default_url_x64"`
	DefaultUrlArm64   string `json:"default_url_arm64"`
	Source            Source `json:"source"`
}

var (
	binLinkDir = "/usr/local/bin"
	desktopDir = "/usr/share/applications"
)

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

	var manifest Manifest
	if *manifestPath != "" {
		manifestData, err := os.ReadFile(*manifestPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] Error reading manifest: %v\n", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(manifestData, &manifest); err != nil {
			fmt.Fprintf(os.Stderr, "[-] Error parsing manifest: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("[*] No manifest provided. Using interactive setup...")
		manifest.AppID = *appIdCli
		if manifest.AppID == "" {
			fmt.Print("[?] Enter App ID (e.g. com.example.app): ")
			fmt.Scanln(&manifest.AppID)
		}
		if manifest.AppID == "" {
			fmt.Fprintln(os.Stderr, "[-] Error: App ID is required.")
			os.Exit(1)
		}

		fmt.Printf("[?] Enter Application Name [%s]: ", manifest.AppID)
		var appName string
		fmt.Scanln(&appName)
		if appName == "" {
			appName = manifest.AppID
		}
		manifest.Name = appName

		fmt.Printf("[?] Enter executable symlink name [%s]: ", manifest.AppID)
		var symlinkName string
		fmt.Scanln(&symlinkName)
		if symlinkName == "" {
			symlinkName = manifest.AppID
		}
		manifest.SymlinkAs = symlinkName

		fmt.Print("[?] Is this a CLI-only application? [y/N]: ")
		var cliAns string
		fmt.Scanln(&cliAns)
		cliAns = strings.ToLower(strings.TrimSpace(cliAns))
		if strings.HasPrefix(cliAns, "y") {
			manifest.CliOnly = true
		} else {
			manifest.CliOnly = false
		}

		manifest.DefaultInstallDir = "/opt/" + manifest.AppID
		manifest.Categories = "Utility;"
	}

	// Input Sanitization
	if strings.Contains(manifest.AppID, "/") || strings.Contains(manifest.AppID, "..") || strings.Contains(manifest.SymlinkAs, "/") || strings.Contains(manifest.SymlinkAs, "..") {
		fmt.Fprintln(os.Stderr, "[-] CRITICAL: Security abort. APP_ID and SYMLINK_NAME cannot contain path traversal characters (/, ..).")
		os.Exit(1)
	}

	fmt.Printf("[*] Auditing environment path structures for %s...\n", manifest.Name)
	binLink := filepath.Join(binLinkDir, manifest.SymlinkAs)
	installDir := manifest.DefaultInstallDir

	if _, err := os.Lstat(binLink); err == nil {
		target, err := filepath.EvalSymlinks(binLink)
		if err == nil {
			installDir = filepath.Dir(target)
			if manifest.BinaryPath != "" && manifest.BinaryPath != "null" {
				depth := strings.Count(manifest.BinaryPath, "/")
				for i := 0; i <= depth; i++ {
					installDir = filepath.Dir(installDir)
				}
			}
			fmt.Printf("[+] Map match: Found existing installation via symlink at %s\n", installDir)
		}
	} else if !*forceInstall {
		fmt.Printf("\n[?] No version found. Perform a clean installation of %s at %s? [Y/n]: ", manifest.Name, installDir)
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
		downloadUrl = resolveDownloadUrl(manifest)
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
		if !fileExists(archivePath) {
			fmt.Fprintf(os.Stderr, "[-] Path fault: Target file missing: %s\n", archivePath)
			os.Exit(1)
		}
	} else if downloadUrl != "" {
		archivePath = downloadFile(downloadUrl, manifest.AppID)
	} else {
		searchDirPath := *searchDir
		if strings.HasPrefix(searchDirPath, "~") && userHome != "" {
			searchDirPath = strings.Replace(searchDirPath, "~", userHome, 1)
		}
		fmt.Printf("[*] Scanning directories under '%s' for updates...\n", searchDirPath)
		
		cmd := exec.Command("sh", "-c", fmt.Sprintf("ls -t %s/*%s*.tar.gz 2>/dev/null | head -n 1", searchDirPath, manifest.AppID))
		out, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			archivePath = strings.TrimSpace(string(out))
		} else {
			fmt.Fprintf(os.Stderr, "[-] Archive fault: No deployment packages matching *%s*.tar.gz found.\n", manifest.AppID)
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
	if err := extractTarGz(archivePath, tmpDir); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error extracting archive: %v\n", err)
		os.Exit(1)
	}

	var localBin string
	if manifest.BinaryPath != "" && manifest.BinaryPath != "null" {
		localBin = filepath.Join(tmpDir, manifest.BinaryPath)
	} else {
		localBin = findBinary(tmpDir, manifest.BinaryPattern)
	}

	restartReqd := false
	if localBin != "" && fileExists(localBin) {
		runningBinName := filepath.Base(localBin)
		escapedBinName := regexp.QuoteMeta(runningBinName)
		pgrepCmd := exec.Command("pgrep", "-u", realUser, "-f", escapedBinName)
		if err := pgrepCmd.Run(); err == nil {
			fmt.Printf("\n[!] Active Process Block: %s is currently running.\n", manifest.Name)
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
			if !manifest.CliOnly {
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
	if err := copyDir(tmpDir, installDir); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error copying files: %v\n", err)
		os.Exit(1)
	}

	var binaryTarget string
	if manifest.BinaryPath != "" && manifest.BinaryPath != "null" {
		binaryTarget = filepath.Join(installDir, manifest.BinaryPath)
	} else {
		binaryTarget = findBinary(installDir, manifest.BinaryPattern)
	}

	if binaryTarget == "" || !fileExists(binaryTarget) {
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

	if !manifest.CliOnly {
		createDesktopEntry(manifest, installDir, binLink, binaryTarget)
	}

	if downloadUrl != "" {
		if *cleanup {
			fmt.Println("[*] Removing compressed remote runtime package artifacts...")
			os.Remove(archivePath)
		} else {
			urlFileName := filepath.Base(downloadUrl)
			urlFileName = strings.ReplaceAll(urlFileName, "%20", " ")
			if strings.HasPrefix(urlFileName, "download") || urlFileName == "" || urlFileName == "." || urlFileName == "/" {
				urlFileName = fmt.Sprintf("%s-linux.tar.gz", manifest.AppID)
			}
			cwd, _ := os.Getwd()
			outputDest := filepath.Join(cwd, urlFileName)
			
			if err := os.Rename(archivePath, outputDest); err != nil {
				copyFile(archivePath, outputDest)
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

	fmt.Printf("\n[+] Success! %s has been deployed via dopt.\n", manifest.Name)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil { return err }
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil { return err }
	defer out.Close()
	_, err = io.Copy(out, in)
	if err != nil { return err }
	return out.Close()
}

func copyDir(src string, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil { return err }
		relPath, err := filepath.Rel(src, path)
		if err != nil { return err }
		targetPath := filepath.Join(dst, relPath)
		
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}
		
		info, err := d.Info()
		if err != nil { return err }
		
		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR, info.Mode())
		if err != nil { return err }
		defer out.Close()
		
		in, err := os.Open(path)
		if err != nil { return err }
		defer in.Close()
		
		_, err = io.Copy(out, in)
		return err
	})
}

func resolveDownloadUrl(m Manifest) string {
	if m.Source.Type == "api" && m.Source.Endpoint != "" {
		resp, err := http.Get(m.Source.Endpoint)
		if err != nil { return "" }
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		
		var v interface{}
		if err := json.Unmarshal(body, &v); err != nil { return "" }
		
		query, err := gojq.Parse(m.Source.JqQuery)
		if err != nil { return "" }
		
		iter := query.Run(v)
		for {
			val, ok := iter.Next()
			if !ok { break }
			if _, isErr := val.(error); isErr { continue }
			if strVal, isStr := val.(string); isStr { return strVal }
		}
	} else if m.Source.Type == "github" && m.Source.Repository != "" {
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", m.Source.Repository)
		resp, err := http.Get(endpoint)
		if err != nil { return "" }
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		
		var release struct {
			Assets []struct {
				BrowserDownloadURL string `json:"browser_download_url"`
			} `json:"assets"`
		}
		if err := json.Unmarshal(body, &release); err != nil { return "" }
		
		pattern := m.Source.AssetPattern
		if pattern == "" {
			arch := "x86_64"
			if runtime.GOARCH == "arm64" { arch = "aarch64" }
			pattern = "linux.*" + arch
		}
		
		for _, asset := range release.Assets {
			matched, _ := filepath.Match("*"+pattern+"*", asset.BrowserDownloadURL)
			if matched || strings.Contains(asset.BrowserDownloadURL, strings.ReplaceAll(pattern, ".*", "")) {
				return asset.BrowserDownloadURL
			}
		}
	}
	
	if runtime.GOARCH == "arm64" && m.DefaultUrlArm64 != "" { return m.DefaultUrlArm64 }
	return m.DefaultUrlX64
}

func downloadFile(url string, appID string) string {
	fmt.Printf("[*] Pulling network distribution payloads from endpoint...\n")
	resp, err := http.Get(url)
	if err != nil || resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "[-] Error: Download gateway failed. Verify network routing or destination URL.\n")
		os.Exit(1)
	}
	defer resp.Body.Close()

	tmpFile := filepath.Join("/tmp", fmt.Sprintf("%s_download.tar.gz", appID))
	out, err := os.Create(tmpFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Failed to create temp file: %v\n", err)
		os.Exit(1)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Failed to write file: %v\n", err)
		os.Exit(1)
	}

	return tmpFile
}

func extractTarGz(tarPath, dest string) error {
	f1, err := os.Open(tarPath)
	if err != nil { return err }
	gzr1, err := gzip.NewReader(f1)
	if err != nil { f1.Close(); return err }
	tr1 := tar.NewReader(gzr1)
	
	var commonPrefix string
	first := true
	for {
		hdr, err := tr1.Next()
		if err == io.EOF || err != nil { break }
		parts := strings.Split(filepath.Clean(hdr.Name), "/")
		if len(parts) == 0 || parts[0] == "" || parts[0] == "." { continue }
		if first {
			commonPrefix = parts[0]
			first = false
		} else if commonPrefix != "" && parts[0] != commonPrefix {
			commonPrefix = ""
		}
	}
	f1.Close()
	
	f2, err := os.Open(tarPath)
	if err != nil { return err }
	defer f2.Close()
	gzr2, err := gzip.NewReader(f2)
	if err != nil { return err }
	defer gzr2.Close()
	tr2 := tar.NewReader(gzr2)
	
	os.MkdirAll(dest, 0755)
	
	for {
		header, err := tr2.Next()
		if err == io.EOF { break }
		if err != nil { return err }

		name := filepath.Clean(header.Name)
		if commonPrefix != "" {
			if strings.HasPrefix(name, commonPrefix+"/") {
				name = strings.TrimPrefix(name, commonPrefix+"/")
			} else if name == commonPrefix {
				continue
			}
		}
		
		target := filepath.Join(dest, name)
		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err == nil {
				io.Copy(out, tr2)
				out.Close()
			}
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(target), 0755)
			os.Symlink(header.Linkname, target)
		}
	}
	return nil
}

func findBinary(baseDir string, pattern string) string {
	var found string
	filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil { return nil }
		rel, _ := filepath.Rel(baseDir, path)
		depth := len(strings.Split(rel, string(os.PathSeparator)))
		if depth > 2 {
			if d.IsDir() { return filepath.SkipDir }
			return nil
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err == nil && info.Mode()&0111 != 0 {
				matched, _ := filepath.Match(strings.ToLower(pattern), strings.ToLower(d.Name()))
				if matched || strings.Contains(strings.ToLower(d.Name()), strings.ToLower(strings.ReplaceAll(pattern, "*", ""))) {
					found = path
					return fmt.Errorf("found") // Stop search
				}
			}
		}
		return nil
	})
	return found
}

func createDesktopEntry(m Manifest, installDir string, binLink string, realBinary string) {
	fmt.Println("[*] Scanning workspace assets for Application Desktop Graphics...")
	
	var iconPath string
	filepath.WalkDir(installDir, func(path string, d os.DirEntry, err error) error {
		if err != nil { return nil }
		if !d.IsDir() {
			name := strings.ToLower(d.Name())
			if strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".svg") {
				if strings.Contains(name, "icon") || strings.Contains(name, strings.ToLower(m.AppID)) {
					iconPath = path
					return fmt.Errorf("found")
				}
			}
		}
		return nil
	})

	if iconPath == "" { iconPath = "system-run" }

	execLine := binLink
	if m.ExecFlags != "" {
		execLine += " " + m.ExecFlags
	}

	content := fmt.Sprintf(`[Desktop Entry]
Version=1.0
Type=Application
Name=%s
Comment=%s
Exec=%s
Icon=%s
Terminal=false
Categories=%s
StartupWMClass=%s
`, m.Name, m.Comment, execLine, iconPath, m.Categories, filepath.Base(realBinary))

	fmt.Printf("[*] Injecting desktop menu shell reference configuration at %s/%s.desktop...\n", desktopDir, m.AppID)
	dest := filepath.Join(desktopDir, fmt.Sprintf("%s.desktop", m.AppID))
	err := os.WriteFile(dest, []byte(content), 0644)
	if err != nil {
		fmt.Printf("[-] Warning: Failed to create desktop entry: %v\n", err)
	} else {
		fmt.Printf("[+] Native Desktop integration verified.\n")
	}
}
