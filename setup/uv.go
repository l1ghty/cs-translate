package setup

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// uv (https://github.com/astral-sh/uv) is a single self-contained binary that
// installs a private Python and the speech worker's packages on demand. Using
// it means users never have to install Python, create virtualenvs or fight
// with pip on any OS.

const uvReleaseURL = "https://github.com/astral-sh/uv/releases/latest/download/"

// uvAsset returns the release archive name for this platform.
func uvAsset() (string, error) {
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" {
		return "", fmt.Errorf("unsupported CPU architecture %s", runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "windows":
		return "uv-" + arch + "-pc-windows-msvc.zip", nil
	case "darwin":
		return "uv-" + arch + "-apple-darwin.tar.gz", nil
	case "linux":
		// statically linked build: works on every distro
		return "uv-" + arch + "-unknown-linux-musl.tar.gz", nil
	}
	return "", fmt.Errorf("unsupported OS %s", runtime.GOOS)
}

// EnsureUV returns the path of a usable uv binary. It prefers an existing
// install, otherwise it downloads a private copy into AppDir()/bin (no admin
// rights, no PATH or shell-profile changes).
func EnsureUV() (string, error) {
	if p := os.Getenv("CS_TRANSLATE_UV"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("uv"); err == nil {
		return p, nil
	}
	dir, err := AppDir()
	if err != nil {
		return "", err
	}
	local := filepath.Join(dir, "bin", exeName("uv"))
	if fileExists(local) {
		return local, nil
	}

	asset, err := uvAsset()
	if err != nil {
		return "", err
	}
	fmt.Printf("Downloading uv (Python runtime manager, ~20 MB) to %s ...\n", filepath.Dir(local))
	if err := installUV(uvReleaseURL, asset, local); err != nil {
		return "", fmt.Errorf("could not install uv: %w\nInstall it manually from https://docs.astral.sh/uv/getting-started/installation/ and run again", err)
	}
	fmt.Println("✔ uv installed")
	return local, nil
}

func installUV(baseURL, asset, dest string) error {
	tmpDir, err := os.MkdirTemp("", "cs-translate-uv")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	archive := filepath.Join(tmpDir, asset)
	if err := downloadFile(baseURL+asset, archive); err != nil {
		return err
	}
	sumFile := archive + ".sha256"
	if err := downloadFile(baseURL+asset+".sha256", sumFile); err != nil {
		return fmt.Errorf("downloading checksum: %w", err)
	}
	if err := verifySHA256(archive, sumFile); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmpDest := dest + ".tmp"
	if strings.HasSuffix(asset, ".zip") {
		err = extractFromZip(archive, "uv.exe", tmpDest) // zip archives are the Windows builds
	} else {
		err = extractFromTarGz(archive, "uv", tmpDest)
	}
	if err != nil {
		os.Remove(tmpDest)
		return err
	}
	return os.Rename(tmpDest, dest)
}

func verifySHA256(file, sumFile string) error {
	raw, err := os.ReadFile(sumFile)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return fmt.Errorf("empty checksum file")
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, fields[0]) {
		return fmt.Errorf("checksum mismatch for %s", filepath.Base(file))
	}
	return nil
}

func extractFromZip(archive, name, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || filepath.Base(f.Name) != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeExecutable(rc, dest)
	}
	return fmt.Errorf("%s not found in %s", name, filepath.Base(archive))
}

func extractFromTarGz(archive, name, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in %s", name, filepath.Base(archive))
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == name {
			return writeExecutable(tr, dest)
		}
	}
}

func writeExecutable(r io.Reader, dest string) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
