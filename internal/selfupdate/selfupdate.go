// Package selfupdate replaces the running binary with a release from GitHub:
// resolve the version, download the archive and checksums.txt, verify the
// SHA-256 of the exact file name, extract exactly one regular file, smoke-test
// it, keep a .rollback copy, then rename it into place.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultBase is where releases live; a fork sets its own with
// -ldflags "-X …/selfupdate.DefaultBase=https://github.com/<fork>/releases" (UPD-03).
// MULTICA_INTAKE_RELEASE_BASE overrides it (tests, mirrors); it must be https
// unless it points at 127.0.0.1.
var DefaultBase = "https://github.com/vantt/multica-intake/releases"

const maxArchive = 64 << 20

// AssetName is the single naming rule shared with package.sh, install.sh and
// the docs (REL-02): multica-intake-<os>-<arch>.tar.gz.
func AssetName(goos, goarch string) string {
	return fmt.Sprintf("multica-intake-%s-%s.tar.gz", goos, goarch)
}

// Base returns the release base URL.
func Base() (string, error) {
	b := strings.TrimRight(os.Getenv("MULTICA_INTAKE_RELEASE_BASE"), "/")
	if b == "" {
		return DefaultBase, nil
	}
	if strings.HasPrefix(b, "https://") || strings.HasPrefix(b, "http://127.0.0.1") || strings.HasPrefix(b, "http://localhost") {
		return b, nil
	}
	return "", errors.New("MULTICA_INTAKE_RELEASE_BASE must be an https URL")
}

func client() *http.Client {
	return &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" && req.URL.Hostname() != "127.0.0.1" && req.URL.Hostname() != "localhost" {
			return errors.New("refusing a redirect to a non-https URL")
		}
		return nil
	}}
}

// Latest resolves the newest release tag through the /latest redirect (no API,
// no rate limit).
func Latest(ctx context.Context) (string, error) {
	base, err := Base()
	if err != nil {
		return "", err
	}
	c := client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/latest", nil)
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("cannot reach %s: %w", base, err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/")
	if resp.StatusCode/100 != 3 || i < 0 {
		return "", fmt.Errorf("could not resolve the latest release from %s/latest (HTTP %d)", base, resp.StatusCode)
	}
	return NormalizeTag(loc[i+5:]), nil
}

// NormalizeTag accepts 1.2.3 or v1.2.3 and returns v1.2.3.
func NormalizeTag(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// Compare returns -1, 0, 1 comparing two vX.Y.Z[-pre] versions; "dev" is oldest.
func Compare(a, b string) int {
	pa, pb := parse(a), parse(b)
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			if pa.n[i] < pb.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	case pa.pre < pb.pre:
		return -1
	}
	return 1
}

type semver struct {
	n   [3]int
	pre string
}

func parse(v string) semver {
	var s semver
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, s.pre, _ = strings.Cut(v, "-")
	for i, p := range strings.SplitN(v, ".", 3) {
		n, err := strconv.Atoi(p)
		if err != nil {
			return semver{n: [3]int{-1, -1, -1}}
		}
		s.n[i] = n
	}
	return s
}

func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: larger than %d bytes", url, limit)
	}
	return b, nil
}

// ExpectedSum finds the SHA-256 of exactly name in a checksums.txt (exactly one entry).
func ExpectedSum(sums []byte, name string) (string, error) {
	var found []string
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			found = append(found, strings.ToLower(f[0]))
		}
	}
	if len(found) != 1 || len(found[0]) != 64 {
		return "", fmt.Errorf("checksums.txt has %d entries for %s (want exactly 1)", len(found), name)
	}
	return found[0], nil
}

// Extract returns the multica-intake executable from a .tar.gz, rejecting any
// unsafe entry (absolute, .., links, devices) and any unexpected file.
func Extract(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	var bin []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(h.Name, "./")
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, `\`) {
			return nil, fmt.Errorf("unsafe path in archive: %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return nil, fmt.Errorf("unexpected entry type in archive: %q", h.Name)
		}
		switch name {
		case "multica-intake":
			if bin != nil {
				return nil, errors.New("archive holds two binaries")
			}
			if bin, err = io.ReadAll(io.LimitReader(tr, maxArchive)); err != nil {
				return nil, err
			}
		case "LICENSE", "README.md", "CHANGELOG.md":
		default:
			if !strings.HasPrefix(name, "completions/") {
				return nil, fmt.Errorf("unexpected file in archive: %q", h.Name)
			}
		}
	}
	if bin == nil {
		return nil, errors.New("archive holds no multica-intake binary")
	}
	return bin, nil
}

// Plan is what an update would do.
type Plan struct {
	Current string `json:"current"`
	Target  string `json:"target"`
	Binary  string `json:"binary"`
	Asset   string `json:"asset"`
	Needed  bool   `json:"needed"`
}

// Prepare resolves the target version.
func Prepare(ctx context.Context, current, want, binary string) (Plan, error) {
	target := NormalizeTag(want)
	if target == "" {
		var err error
		if target, err = Latest(ctx); err != nil {
			return Plan{}, err
		}
	}
	p := Plan{Current: current, Target: target, Binary: binary, Asset: AssetName(runtime.GOOS, runtime.GOARCH)}
	p.Needed = want != "" && Compare(target, current) != 0 || Compare(target, current) > 0
	return p, nil
}

// Apply downloads, verifies and swaps in p.Target. The old binary stays as
// <binary>.rollback until the new one has answered --version.
func Apply(ctx context.Context, p Plan) error {
	base, err := Base()
	if err != nil {
		return err
	}
	dl := base + "/download/" + p.Target + "/"
	sums, err := get(ctx, dl+"checksums.txt", 1<<20)
	if err != nil {
		return err
	}
	want, err := ExpectedSum(sums, p.Asset)
	if err != nil {
		return err
	}
	archive, err := get(ctx, dl+p.Asset, maxArchive)
	if err != nil {
		return err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s: the download is not the published file; nothing was changed", p.Asset)
	}
	bin, err := Extract(archive)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.Binary)
	tmp, err := os.CreateTemp(dir, ".multica-intake-new-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s: %w", p.Binary, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, tmp.Name(), "--version").Output()
	if err != nil || !strings.Contains(string(out), strings.TrimPrefix(p.Target, "v")) {
		return fmt.Errorf("the new binary failed its smoke test (%q); nothing was changed", strings.TrimSpace(string(out)))
	}
	rollback := p.Binary + ".rollback"
	if err := copyFile(p.Binary, rollback); err != nil {
		return fmt.Errorf("cannot keep a rollback copy: %w", err)
	}
	if err := os.Rename(tmp.Name(), p.Binary); err != nil {
		return err
	}
	return nil
}

// Rollback restores <binary>.rollback.
func Rollback(binary string) error {
	rb := binary + ".rollback"
	if _, err := os.Stat(rb); err != nil {
		return errors.New("there is no rollback copy")
	}
	return os.Rename(rb, binary)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}
