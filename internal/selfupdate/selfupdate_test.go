package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func tgz(t *testing.T, files map[string]string, typ byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		h := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: typ}
		if typ == tar.TypeSymlink {
			h.Size, h.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			tw.Write([]byte(body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{{"v1.2.3", "1.2.3", 0}, {"v1.2.3", "v1.10.0", -1}, {"v2.0.0", "v1.9.9", 1}, {"dev", "v0.0.1", -1},
		{"v1.0.0-rc.1", "v1.0.0", -1}, {"v1.0.0", "v1.0.0-rc.1", 1}}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s,%s)=%d want %d", c.a, c.b, got, c.want)
		}
	}
	if NormalizeTag("1.2.3") != "v1.2.3" || NormalizeTag("v1.2.3") != "v1.2.3" {
		t.Error("NormalizeTag")
	}
	if AssetName("linux", "amd64") != "multica-intake-linux-amd64.tar.gz" {
		t.Error("AssetName")
	}
}

func TestExpectedSumNeedsExactlyOneExactName(t *testing.T) {
	sum := "aa" + fmt.Sprintf("%062d", 0)
	s := []byte(sum + "  multica-intake-linux-amd64.tar.gz\n" + sum + "  multica-intake-linux-amd64.tar.gz.sig\n")
	if got, err := ExpectedSum(s, "multica-intake-linux-amd64.tar.gz"); err != nil || got != sum {
		t.Fatalf("%s %v", got, err)
	}
	if _, err := ExpectedSum(s, "linux-amd64.tar.gz"); err == nil {
		t.Fatal("substring matched")
	}
	if _, err := ExpectedSum(append(s, s...), "multica-intake-linux-amd64.tar.gz"); err == nil {
		t.Fatal("two entries accepted")
	}
}

func TestExtractRejectsUnsafeArchives(t *testing.T) {
	if b, err := Extract(tgz(t, map[string]string{"multica-intake": "BIN", "LICENSE": "x"}, tar.TypeReg)); err != nil || string(b) != "BIN" {
		t.Fatalf("good archive: %v", err)
	}
	for name, files := range map[string]map[string]string{
		"dotdot":   {"../multica-intake": "x"},
		"absolute": {"/multica-intake": "x"},
		"extra":    {"multica-intake": "x", "evil.sh": "x"},
		"empty":    {"LICENSE": "x"},
	} {
		if _, err := Extract(tgz(t, files, tar.TypeReg)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := Extract(tgz(t, map[string]string{"multica-intake": ""}, tar.TypeSymlink)); err == nil {
		t.Error("symlink accepted")
	}
}

func TestApplySwapsVerifiedBinaryAndKeepsRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script binary")
	}
	newBin := "#!/bin/sh\necho 'multica-intake v9.9.9 (test)'\n"
	archive := tgz(t, map[string]string{"multica-intake": newBin}, tar.TypeReg)
	asset := AssetName(runtime.GOOS, runtime.GOARCH)
	h := sha256.Sum256(archive)
	sums := hex.EncodeToString(h[:]) + "  " + asset + "\n"
	bad := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "/tag/v9.9.9", http.StatusFound)
		case "/download/v9.9.9/checksums.txt":
			w.Write([]byte(sums))
		case "/download/v9.9.9/" + asset:
			if bad {
				w.Write(append([]byte{}, archive[:len(archive)-1]...))
				return
			}
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("MULTICA_INTAKE_RELEASE_BASE", srv.URL)

	dir := t.TempDir()
	bin := filepath.Join(dir, "multica-intake")
	os.WriteFile(bin, []byte("#!/bin/sh\necho old\n"), 0o755)
	ctx := context.Background()
	if v, err := Latest(ctx); err != nil || v != "v9.9.9" {
		t.Fatalf("latest %s %v", v, err)
	}
	plan, err := Prepare(ctx, "v1.0.0", "", bin)
	if err != nil || !plan.Needed || plan.Target != "v9.9.9" {
		t.Fatalf("plan %+v %v", plan, err)
	}
	bad = true
	if err := Apply(ctx, plan); err == nil {
		t.Fatal("corrupt download applied")
	}
	if b, _ := os.ReadFile(bin); string(b) != "#!/bin/sh\necho old\n" {
		t.Fatal("binary changed after a failed update")
	}
	bad = false
	if err := Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != newBin {
		t.Fatal("binary not swapped")
	}
	if err := Rollback(bin); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "#!/bin/sh\necho old\n" {
		t.Fatal("rollback did not restore")
	}
	if p, _ := Prepare(ctx, "v9.9.9", "", bin); p.Needed {
		t.Fatal("same version should not need an update")
	}
}

func TestBaseMustBeHTTPS(t *testing.T) {
	t.Setenv("MULTICA_INTAKE_RELEASE_BASE", "http://example.com/r")
	if _, err := Base(); err == nil {
		t.Fatal("plain http accepted")
	}
}
