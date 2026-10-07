package archive

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ExtractTarGz(tarPath, dest string) error {
	f1, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	gzr1, err := gzip.NewReader(f1)
	if err != nil {
		f1.Close()
		return err
	}
	tr1 := tar.NewReader(gzr1)

	var commonPrefix string
	first := true
	for {
		hdr, err := tr1.Next()
		if err == io.EOF || err != nil {
			break
		}
		parts := strings.Split(filepath.Clean(hdr.Name), "/")
		if len(parts) == 0 || parts[0] == "" || parts[0] == "." {
			continue
		}
		if first {
			commonPrefix = parts[0]
			first = false
		} else if commonPrefix != "" && parts[0] != commonPrefix {
			commonPrefix = ""
		}
	}
	f1.Close()

	f2, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f2.Close()
	gzr2, err := gzip.NewReader(f2)
	if err != nil {
		return err
	}
	defer gzr2.Close()
	tr2 := tar.NewReader(gzr2)

	os.MkdirAll(dest, 0755)

	for {
		header, err := tr2.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

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
