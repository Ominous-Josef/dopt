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
	"path/filepath"
	"runtime"
	"strings"

	"github.com/itchyny/gojq"
)

type Source struct {
	Type         string `json:"type"`
	Endpoint     string `json:"endpoint,omitempty"`
	JqQuery      string `json:"jq_query,omitempty"`
	Repository   string `json:"repository,omitempty"`
	AssetPattern string `json:"asset_pattern,omitempty"`
}

// Manifest represents the structure of our application recipe blueprint.
// By parsing this in Go natively, we eliminate the need for `jq`.
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
	// 1. Setup Command-Line Flags
	manifestPath := flag.String("m", "", "The application manifest recipe configuration file")
	flag.StringVar(manifestPath, "manifest", "", "The application manifest recipe configuration file")
	
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

	// 2. Initial Validation
	if *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "[-] Error: A valid application manifest file path is required (-m / --manifest).")
		os.Exit(1)
	}

	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "[-] Error: dopt engine modifications require root context. Re-run command using sudo.")
		os.Exit(1)
	}

	// 3. Native JSON Parsing (Replaces jq)
	manifestData, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error reading manifest: %v\n", err)
		os.Exit(1)
	}

	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error parsing manifest: %v\n", err)
		os.Exit(1)
	}

	// 4. Resolve Environment Paths
	fmt.Printf("[*] Auditing environment path structures for %s...\n", manifest.Name)
	binLink := filepath.Join(binLinkDir, manifest.SymlinkAs)
	installDir := manifest.DefaultInstallDir

	if _, err := os.Lstat(binLink); err == nil {
		target, err := filepath.EvalSymlinks(binLink)
		if err == nil {
			installDir = filepath.Dir(target)
			if manifest.BinaryPath != "" && manifest.BinaryPath != "null" {
				depth := strings.Count(manifest.BinaryPath, "/")
				for i := 0; i < depth; i++ {
					installDir = filepath.Dir(installDir)
				}
			}
			fmt.Printf("[+] Map match: Found existing installation via symlink at %s\n", installDir)
		}
	} else if !*forceInstall {
		fmt.Printf("\n[?] No version found. Perform a clean installation of %s at %s? [Y/n]: ", manifest.Name, installDir)
		var response string
		fmt.Scanln(&response)
		if response == "n" || response == "no" {
			fmt.Println("[-] Deployment aborted.")
			os.Exit(0)
		}
	}

	// 5. Download URL Resolution
	var downloadUrl string
	if *customUrl != "" {
		downloadUrl = *customUrl
	} else if *download || *forceInstall {
		downloadUrl = resolveDownloadUrl(manifest)
		if downloadUrl == "" {
			// Do not fail immediately, maybe fallback to local
			fmt.Println("[-] Warning: Failed to resolve dynamic download URL or none provided.")
		} else {
			fmt.Printf("[+] Resolved download URL: %s\n", downloadUrl)
		}
	}

	var archivePath string
	if *filePath != "" {
		archivePath = *filePath
	} else if downloadUrl != "" {
		archivePath = downloadFile(downloadUrl, manifest.AppID)
		if *cleanup {
			defer os.Remove(archivePath)
		}
	} else {
		fmt.Printf("[*] Scanning %s for archive...\n", *searchDir)
		entries, err := os.ReadDir(*searchDir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".tar.gz") && strings.Contains(e.Name(), manifest.AppID) {
					archivePath = filepath.Join(*searchDir, e.Name())
					break
				}
			}
		}
	}

	if archivePath == "" {
		fmt.Fprintln(os.Stderr, "[-] Error: No archive file provided or found.")
		os.Exit(1)
	}

	// 6. Extraction
	fmt.Printf("[*] Extracting %s to %s...\n", archivePath, installDir)
	if err := extractTarGz(archivePath, installDir); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error extracting archive: %v\n", err)
		os.Exit(1)
	}

	// 7. Find Binary and Link
	var binaryTarget string
	if manifest.BinaryPath != "" && manifest.BinaryPath != "null" {
		binaryTarget = filepath.Join(installDir, manifest.BinaryPath)
	} else {
		binaryTarget = findBinary(installDir, manifest.BinaryPattern)
	}
	
	if binaryTarget == "" || !fileExists(binaryTarget) {
		fmt.Fprintf(os.Stderr, "[-] Error: Could not locate binary %s inside %s\n", manifest.BinaryPattern, installDir)
		os.Exit(1)
	}
	
	// Create symlink
	os.Remove(binLink)
	if err := os.Symlink(binaryTarget, binLink); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Error creating symlink: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[+] Symlink created: %s -> %s\n", binLink, binaryTarget)

	// 8. Desktop Entry
	if !manifest.CliOnly {
		createDesktopEntry(manifest, installDir, binaryTarget)
	}

	fmt.Println("[+] Setup completed successfully.")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
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
	fmt.Printf("[*] Downloading from %s...\n", url)
	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Download failed: %v\n", err)
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
	
	os.RemoveAll(dest)
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

func createDesktopEntry(m Manifest, installDir string, binPath string) {
	fmt.Println("[*] Generating desktop entry...")
	
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

	content := fmt.Sprintf(`[Desktop Entry]
Version=1.0
Type=Application
Name=%s
Comment=%s
Exec=%s %s
Icon=%s
Terminal=false
Categories=%s
StartupWMClass=%s
`, m.Name, m.Comment, binPath, m.ExecFlags, iconPath, m.Categories, filepath.Base(binPath))

	dest := filepath.Join(desktopDir, fmt.Sprintf("%s.desktop", m.AppID))
	err := os.WriteFile(dest, []byte(content), 0644)
	if err != nil {
		fmt.Printf("[-] Warning: Failed to create desktop entry: %v\n", err)
	} else {
		fmt.Printf("[+] Desktop entry created at %s\n", dest)
	}
}
