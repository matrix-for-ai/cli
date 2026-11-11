package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fakeCurl = `#!/bin/sh
while [ $# -gt 0 ]; do
  case $1 in
    -o) out=$2; shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
case $url in
  https://github.com/matrix-for-ai/cli/releases/latest/download/*) cp "$RELEASE_DIR/${url##*/}" "$out" ;;
  *) exit 22 ;;
esac
`

func runInstall(t *testing.T, sums func(binary []byte) string) (home, out string, err error) {
	t.Helper()
	dir := t.TempDir()
	release, fakeBin := filepath.Join(dir, "release"), filepath.Join(dir, "bin")
	home = filepath.Join(dir, "home")
	for _, d := range []string{release, fakeBin, home} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	name := fmt.Sprintf("matrix-for-ai-cli-%s-%s", runtime.GOOS, runtime.GOARCH)
	binary := []byte("#!/bin/sh\necho matrix-for-ai-cli 9.9.9\n")
	write := func(path, content string, mode os.FileMode) {
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(release, name), string(binary), 0o644)
	write(filepath.Join(release, "SHA256SUMS"), sums(binary)+"  "+name+"\n", 0o644)
	write(filepath.Join(fakeBin, "curl"), fakeCurl, 0o755)

	cmd := exec.Command("sh", "../../install.sh")
	cmd.Env = append(os.Environ(), "HOME="+home, "RELEASE_DIR="+release, "PATH="+fakeBin+":"+os.Getenv("PATH"))
	b, err := cmd.CombinedOutput()
	return home, string(b), err
}

func TestInstall(t *testing.T) {
	home, out, err := runInstall(t, func(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) })
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	bindir := filepath.Join(home, ".local", "bin")
	if want := "installed matrix-for-ai-cli 9.9.9 in " + bindir + "\n" + bindir + " is not in your PATH: add it\n"; out != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	info, err := os.Stat(filepath.Join(bindir, "matrix-for-ai-cli"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want 0755", info.Mode().Perm())
	}
}

func TestInstallRefusesAMismatchedSum(t *testing.T) {
	home, out, err := runInstall(t, func([]byte) string { return strings.Repeat("0", 64) })
	if err == nil {
		t.Fatalf("install succeeded:\n%s", out)
	}
	if !strings.Contains(out, "does not match SHA256SUMS") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "matrix-for-ai-cli")); !os.IsNotExist(err) {
		t.Errorf("binary installed despite the mismatch: %v", err)
	}
}
