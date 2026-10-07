package utils

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Close()
}

func CopyDir(src string, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR, info.Mode())
		if err != nil {
			return err
		}
		defer out.Close()

		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()

		_, err = io.Copy(out, in)
		return err
	})
}

func FindBinary(baseDir string, pattern string) string {
	var found string
	filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(baseDir, path)
		depth := len(strings.Split(rel, string(os.PathSeparator)))
		if depth > 2 {
			if d.IsDir() {
				return filepath.SkipDir
			}
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
