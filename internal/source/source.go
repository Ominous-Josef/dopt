// Package source finds and fetches app archives: it resolves download URLs
// from manifests, downloads them, verifies checksums, and keeps downloaded
// archives without ever overwriting a file.
package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/itchyny/gojq"

	"github.com/Ominous-Josef/dopt/internal/archive"
	"github.com/Ominous-Josef/dopt/internal/manifest"
)

// GitHubAPI is the GitHub API base URL (replaced in tests).
var GitHubAPI = "https://api.github.com"

// UserAgent is sent with every request.
var UserAgent = "dopt"

// Client has connection timeouts but no overall deadline, so large downloads can finish.
var Client = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

func get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s returned %s", redact(rawURL), resp.Status)
	}
	return resp, nil
}

// redact drops the query string, which may hold tokens, from URLs shown to the user.
func redact(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.RawQuery != "" {
		u.RawQuery = ""
		return u.String() + "?…"
	}
	return rawURL
}

func getJSON(ctx context.Context, rawURL string) (any, error) {
	resp, err := get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&v); err != nil {
		return nil, fmt.Errorf("%s didn't return valid JSON: %w", redact(rawURL), err)
	}
	return v, nil
}

// goArch maps dopt's architecture names back to Go's ($goarch in jq queries).
var goArch = map[string]string{"x64": "amd64", "arm64": "arm64"}

// archWords are the spellings vendors use for each architecture in asset names.
var archWords = map[string][]string{
	"x64":   {"x86_64", "amd64", "x64", "linux64"},
	"arm64": {"aarch64", "arm64"},
}

// Resolved is a download URL found from a manifest.
type Resolved struct {
	URL     string
	Version string // release name when the source reports one (GitHub tag), else ""
}

// Resolve returns the download URL for arch ("x64" or "arm64"): from the
// manifest's source when it has one, otherwise its fixed default_url_*.
func Resolve(ctx context.Context, m manifest.Manifest, arch string) (Resolved, error) {
	if m.Source != nil {
		switch m.Source.Type {
		case "api":
			u, err := resolveAPI(ctx, m.Source, arch)
			return Resolved{URL: u}, err
		case "github":
			return resolveGitHub(ctx, m.Source, arch)
		}
		return Resolved{}, fmt.Errorf("unknown source type %q", m.Source.Type)
	}
	if u := m.DefaultURL(arch); u != "" {
		return Resolved{URL: u}, nil
	}
	return Resolved{}, fmt.Errorf("the manifest has no download URL for %s. Use -u <url>", arch)
}

func resolveAPI(ctx context.Context, s *manifest.Source, arch string) (string, error) {
	query, err := gojq.Parse(s.JqQuery)
	if err != nil {
		return "", fmt.Errorf("invalid jq_query: %w", err)
	}
	code, err := gojq.Compile(query, gojq.WithVariables([]string{"$arch", "$goarch", "$os"}))
	if err != nil {
		return "", fmt.Errorf("invalid jq_query: %w", err)
	}
	data, err := getJSON(ctx, s.Endpoint)
	if err != nil {
		return "", err
	}
	iter := code.RunWithContext(ctx, data, arch, goArch[arch], "linux")
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return "", fmt.Errorf("jq_query failed: %w", err)
		}
		if str, isStr := v.(string); isStr && str != "" {
			return str, nil
		}
	}
	return "", fmt.Errorf("jq_query returned no URL from %s", redact(s.Endpoint))
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func resolveGitHub(ctx context.Context, s *manifest.Source, arch string) (Resolved, error) {
	resp, err := get(ctx, fmt.Sprintf("%s/repos/%s/releases/latest", GitHubAPI, s.Repository))
	if err != nil {
		return Resolved{}, err
	}
	defer resp.Body.Close()
	var rel githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&rel); err != nil {
		return Resolved{}, fmt.Errorf("unexpected GitHub response: %w", err)
	}
	found := func(u string) (Resolved, error) { return Resolved{URL: u, Version: rel.TagName}, nil }

	pattern := s.AssetPattern
	switch {
	case arch == "x64" && s.AssetPatternX64 != "":
		pattern = s.AssetPatternX64
	case arch == "arm64" && s.AssetPatternArm64 != "":
		pattern = s.AssetPatternArm64
	}
	for _, a := range rel.Assets {
		if pattern != "" && matchAsset(pattern, a.Name) && archive.HasArchiveExt(a.Name) {
			return found(a.URL)
		}
	}
	// Without a pattern: a Linux download that names this architecture, preferring
	// formats in the order of archive.Extensions (.tar.gz first).
	if pattern == "" {
		for _, ext := range archive.Extensions {
			for _, a := range rel.Assets {
				lower := strings.ToLower(a.Name)
				if strings.HasSuffix(a.Name, ext) && strings.Contains(lower, "linux") && containsAny(lower, archWords[arch]) {
					return found(a.URL)
				}
			}
		}
	}
	names := make([]string, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		names = append(names, a.Name)
	}
	if pattern == "" {
		pattern = "(automatic)"
	}
	return Resolved{}, fmt.Errorf("no asset in %s %s matches %q for %s (assets: %s)", s.Repository, rel.TagName, pattern, arch, strings.Join(names, ", "))
}

// matchAsset treats patterns with glob characters as globs and others as substrings.
func matchAsset(pattern, name string) bool {
	if strings.ContainsAny(pattern, "*?[") {
		ok, _ := path.Match(pattern, name)
		return ok
	}
	return strings.Contains(name, pattern)
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// Progress receives download progress; total is -1 when the size is unknown.
type Progress func(done, total int64)

// Fetched describes what a URL served, to recognize the release later.
type Fetched struct {
	URL          string // as requested
	FinalURL     string // after redirects
	ETag         string
	LastModified string
	Length       int64 // -1 if unknown
}

func fetched(rawURL string, resp *http.Response) Fetched {
	return Fetched{
		URL:          rawURL,
		FinalURL:     resp.Request.URL.String(),
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		Length:       resp.ContentLength,
	}
}

// Download saves rawURL to dest. On failure nothing is left at dest.
func Download(ctx context.Context, rawURL, dest string, progress Progress) (info Fetched, err error) {
	resp, err := get(ctx, rawURL)
	if err != nil {
		return info, err
	}
	defer resp.Body.Close()
	info = fetched(rawURL, resp)

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return info, err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dest)
		}
	}()

	var w io.Writer = f
	if progress != nil {
		w = &progressWriter{w: f, total: resp.ContentLength, report: progress}
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return info, fmt.Errorf("download interrupted: %w", err)
	}
	return info, nil
}

// Probe asks what rawURL currently serves without downloading it: a HEAD
// request, or a one-byte GET for servers that refuse HEAD (such as signed CDN URLs).
func Probe(ctx context.Context, rawURL string) (Fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return Fetched{}, err
	}
	req.Header.Set("User-Agent", UserAgent)
	if resp, err := Client.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return fetched(rawURL, resp), nil
		}
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := Client.Do(req)
	if err != nil {
		return Fetched{}, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return Fetched{}, fmt.Errorf("%s returned %s", redact(rawURL), resp.Status)
	}
	info := fetched(rawURL, resp)
	if resp.StatusCode == http.StatusPartialContent {
		info.Length = -1
		if _, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/"); ok {
			fmt.Sscan(total, &info.Length)
		}
	}
	return info, nil
}

var versionPattern = regexp.MustCompile(`\d+(?:\.\d+)+`)

// VersionFromURL finds a version number like 1.27.1 in a URL's path,
// looking at the file name first, then the folders above it.
func VersionFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(u.Path, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if v := versionPattern.FindString(parts[i]); v != "" {
			return v
		}
	}
	return ""
}

func withoutQuery(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		u.RawQuery, u.Fragment = "", ""
		return u.String()
	}
	return rawURL
}

// Fingerprint identifies the release behind a download, so a later check can tell
// whether it changed. It prefers a versioned URL (requested or after redirects),
// then the server's ETag, then Last-Modified and size. reliable is false when
// none of these exist and changes can't be detected.
func Fingerprint(f Fetched) (fp string, reliable bool) {
	for _, u := range []string{f.URL, f.FinalURL} {
		if u != "" && VersionFromURL(u) != "" {
			return "url:" + withoutQuery(u), true
		}
	}
	if f.ETag != "" {
		return "etag:" + f.ETag, true
	}
	if f.LastModified != "" {
		return fmt.Sprintf("modified:%s:%d", f.LastModified, f.Length), true
	}
	return "url:" + withoutQuery(f.FinalURL), false
}

// Version is the best human-readable version for a release: the source's own
// (GitHub tag), else one found in the requested or final URL.
func Version(r Resolved, f Fetched) string {
	if r.Version != "" {
		return r.Version
	}
	if v := VersionFromURL(f.URL); v != "" {
		return v
	}
	return VersionFromURL(f.FinalURL)
}

type progressWriter struct {
	w           io.Writer
	done, total int64
	last        time.Time
	report      Progress
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if now := time.Now(); now.Sub(p.last) > 100*time.Millisecond || p.done == p.total {
		p.last = now
		p.report(p.done, p.total)
	}
	return n, err
}

// SHA256 returns the hex SHA-256 of the file at p.
func SHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ErrChecksum means the archive's SHA-256 didn't match.
var ErrChecksum = errors.New("SHA-256 mismatch")

// VerifySHA256 checks p against the expected lowercase hex checksum.
func VerifySHA256(p, expected string) (actual string, err error) {
	actual, err = SHA256(p)
	if err != nil {
		return "", err
	}
	if actual != expected {
		return actual, ErrChecksum
	}
	return actual, nil
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9._ +-]+$`)

// SaveName picks the filename for keeping a download from rawURL: the URL's
// last path segment if it is a plain name with a supported extension, else
// <appID>-linux<ext> (ext from the detected format).
func SaveName(rawURL, appID, ext string) string {
	name := rawURL
	if i := strings.IndexAny(name, "?#"); i >= 0 {
		name = name[:i]
	}
	name = path.Base(name)
	if unescaped, err := url.PathUnescape(name); err == nil {
		name = unescaped
	}
	if name == "" || name == "/" || name == "." || strings.HasPrefix(name, "download") ||
		strings.HasPrefix(name, ".") || !safeName.MatchString(name) || !archive.HasArchiveExt(name) {
		return appID + "-linux" + ext
	}
	return name
}

// splitExt splits name into stem and a known extension (".tar.gz", ".zip", ...).
func splitExt(name string) (string, string) {
	for _, ext := range archive.Extensions {
		if strings.HasSuffix(name, ext) {
			return strings.TrimSuffix(name, ext), ext
		}
	}
	if ext := path.Ext(name); ext != "" {
		return strings.TrimSuffix(name, ext), ext
	}
	return name, ""
}

// Keep moves the downloaded file src into dir under SaveName, never overwriting:
// an identical existing copy is reused, otherwise name-1, name-2, ... is used.
// It refuses downloads that aren't readable. uid/gid >= 0 sets the new file's owner.
func Keep(src, rawURL, appID, dir string, uid, gid int) (string, error) {
	if err := archive.Valid(src); err != nil {
		return "", fmt.Errorf("not keeping an unreadable download: %w", err)
	}
	format, _ := archive.Detect(src)
	name := SaveName(rawURL, appID, format.Ext())
	stem, ext := splitExt(name)
	for n := 0; ; n++ {
		dest := filepath.Join(dir, name)
		if n > 0 {
			dest = filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, n, ext))
		}
		if _, err := os.Lstat(dest); err == nil {
			if same, _ := sameContent(src, dest); same {
				return dest, nil
			}
			continue
		}
		if err := moveNoReplace(src, dest); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", err
		}
		if uid >= 0 {
			os.Lchown(dest, uid, gid)
		}
		return dest, nil
	}
}

// moveNoReplace moves src to dest, failing with os.ErrExist if dest appears meanwhile.
func moveNoReplace(src, dest string) error {
	// A hard link never replaces an existing file; it fails across filesystems.
	if err := os.Link(src, dest); err == nil {
		return os.Remove(src)
	} else if errors.Is(err, os.ErrExist) {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return err
	}
	return os.Remove(src)
}

func sameContent(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil || !ib.Mode().IsRegular() || ia.Size() != ib.Size() {
		return false, err
	}
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	bufA, bufB := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, errA := io.ReadFull(fa, bufA)
		nb, errB := io.ReadFull(fb, bufB)
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
		if errA == io.EOF || errA == io.ErrUnexpectedEOF {
			return errB == io.EOF || errB == io.ErrUnexpectedEOF, nil
		}
		if errA != nil {
			return false, errA
		}
		if errB != nil {
			return false, errB
		}
	}
}
