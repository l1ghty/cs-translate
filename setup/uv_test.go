package setup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func tarGz(t *testing.T, name string, body []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "uv-x/", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "uv-x/uvx", Typeflag: tar.TypeReg, Mode: 0o755, Size: 3})
	tw.Write([]byte("uvx"))
	tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipFile(t *testing.T, name string, body []byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("uvx.exe")
	w.Write([]byte("uvx"))
	w, _ = zw.Create(name)
	w.Write(body)
	zw.Close()
	return buf.Bytes()
}

func serve(t *testing.T, asset string, data []byte, sum string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + asset:
			w.Write(data)
		case "/" + asset + ".sha256":
			w.Write([]byte(sum + "  " + asset + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestInstallUV(t *testing.T) {
	bin := []byte("#!/bin/sh\necho uv\n")
	for _, tc := range []struct {
		asset, inner string
		data         []byte
	}{
		{"uv-x86_64-unknown-linux-musl.tar.gz", "uv-x86_64-unknown-linux-musl/uv", tarGz(t, "uv-x86_64-unknown-linux-musl/uv", bin)},
		{"uv-x86_64-pc-windows-msvc.zip", "uv.exe", zipFile(t, "uv.exe", bin)},
	} {
		srv := serve(t, tc.asset, tc.data, sha(tc.data))
		dest := filepath.Join(t.TempDir(), "bin", "uv")
		if filepath.Ext(tc.asset) == ".zip" {
			dest += ".exe"
		}
		if err := installUV(srv.URL+"/", tc.asset, dest); err != nil {
			t.Fatalf("%s: %v", tc.asset, err)
		}
		got, err := os.ReadFile(dest)
		if err != nil || !bytes.Equal(got, bin) {
			t.Fatalf("%s: extracted %q, %v", tc.asset, got, err)
		}
		if st, _ := os.Stat(dest); st.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s: not executable", tc.asset)
		}
	}
}

func TestInstallUVRejectsBadChecksum(t *testing.T) {
	data := tarGz(t, "d/uv", []byte("evil"))
	asset := "uv-x86_64-unknown-linux-musl.tar.gz"
	srv := serve(t, asset, data, sha([]byte("something else")))
	dest := filepath.Join(t.TempDir(), "uv")
	if err := installUV(srv.URL+"/", asset, dest); err == nil {
		t.Fatal("expected checksum error")
	}
	if fileExists(dest) {
		t.Fatal("binary installed despite bad checksum")
	}
}

func TestUVAsset(t *testing.T) {
	if a, err := uvAsset(); err != nil || a == "" {
		t.Fatalf("uvAsset() = %q, %v", a, err)
	}
}
