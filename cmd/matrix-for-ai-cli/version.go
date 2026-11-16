package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var releasesURL = "https://github.com/matrix-for-ai/cli/releases"

func cmdVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	check := fs.Bool("check", false, "")
	parse(fs, args)
	if err := need(fs); err != nil {
		return err
	}
	fmt.Printf("matrix-for-ai-cli %s (appservice API v%d)\n", version, appserviceAPIVersion)
	if !*check {
		return nil
	}
	return checkUpdate(os.Stdout)
}

func checkUpdate(w io.Writer) error {
	latest, err := latestRelease()
	if err != nil {
		return err
	}
	if !newer(latest, version) {
		fmt.Fprintln(w, "up to date")
		return nil
	}
	fmt.Fprintf(w, "%s is out: %s/tag/v%[1]s\n", latest, releasesURL)
	if runtime.GOOS == "windows" {
		fmt.Fprintf(w, "To update: download matrix-for-ai-cli-windows-%s.exe from %s/latest\n", runtime.GOARCH, releasesURL)
	} else {
		fmt.Fprintf(w, "To update: curl -fsSL %s/latest/download/install.sh | sh\n", releasesURL)
	}
	fmt.Fprintln(w, "Then, if the receiver runs as a service: matrix-for-ai-cli service install")
	return nil
}

// latestRelease reads the version from where releases/latest redirects: no API, no rate limit.
func latestRelease() (string, error) {
	c := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(releasesURL + "/latest")
	if err != nil {
		return "", fmt.Errorf("latest release: %w", err)
	}
	resp.Body.Close()
	_, v, ok := strings.Cut(resp.Header.Get("Location"), "/tag/v")
	if !ok {
		return "", fmt.Errorf("latest release: %s answered %s, with no release to redirect to", releasesURL, resp.Status)
	}
	return v, nil
}

// newer: whether version a is after b, both X.Y.Z.
func newer(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x > y
		}
	}
	return len(as) > len(bs)
}
