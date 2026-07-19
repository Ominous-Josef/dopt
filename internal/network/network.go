package network

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/itchyny/gojq"
	"github.com/Ominous-Josef/dopt/internal/manifest"
)

func ResolveDownloadUrl(m manifest.Manifest) string {
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

func DownloadFile(url string, appID string) string {
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
