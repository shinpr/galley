// Package updatecheck runs the interactive CLI's advisory update check: at most
// one bounded GitHub latest-release request per 24 hours when stderr is a TTY.
package updatecheck

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shinpr/galley/internal/galleyhome"
	"github.com/shinpr/galley/internal/version"
	"golang.org/x/term"
)

const (
	// Endpoint is the official GitHub latest-release endpoint for Galley.
	Endpoint = "https://api.github.com/repos/shinpr/galley/releases/latest"
	// Interval is the fixed cadence between recorded attempts.
	Interval = 24 * time.Hour
	// Timeout is the dedicated bound for the single network request.
	Timeout = 2 * time.Second

	stateFileName = "update-check.json"
)

// Options injects the check's dependencies. Zero values resolve to the real
// environment (default root, time.Now, stderr TTY probe, embedded version).
type Options struct {
	Root           string
	Now            func() time.Time
	IsTTY          func() bool
	Do             func(*http.Request) (*http.Response, error)
	CurrentVersion string
	Stderr         io.Writer
	Endpoint       string
}

// state is the Galley-owned attempt record persisted under the root.
type state struct {
	LastAttempt time.Time `json:"last_attempt"`
}

type latestRelease struct {
	TagName string `json:"tag_name"`
}

// Run performs one advisory update check. It never reports an error and never
// writes anywhere except the state file and the configured stderr writer.
func Run(opts Options) {
	isTTY := opts.IsTTY
	if isTTY == nil {
		isTTY = stderrIsTTY
	}
	if !isTTY() {
		return
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	root := opts.Root
	if root == "" {
		root = galleyhome.DefaultRoot()
	}
	if fresh, err := readState(root, now()); err == nil && fresh {
		return
	}
	// The attempt is recorded before the request so success and failure both
	// rate-limit; when persistence fails the network request is skipped.
	if err := writeState(root, now()); err != nil {
		return
	}
	tag, ok := fetchLatestTag(opts)
	if !ok {
		return
	}
	current := opts.CurrentVersion
	if current == "" {
		current = version.Version
	}
	if !isNewer(current, tag) {
		return
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintf(stderr, "galley update available: %s -> %s; update instructions: https://github.com/shinpr/galley#manual-cli-installation\n", current, tag)
}

// stderrIsTTY reports whether stderr is a real terminal, so redirected output
// such as /dev/null, files, and pipes keeps the check ineligible.
func stderrIsTTY() bool {
	return fileIsTerminal(os.Stderr)
}

func fileIsTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func readState(root string, now time.Time) (fresh bool, err error) {
	data, err := os.ReadFile(filepath.Join(root, stateFileName))
	if err != nil {
		return false, err
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return false, nil
	}
	// A future-dated record (negative age, e.g. after a clock rollback) is not
	// fresh; only a non-negative age strictly inside the interval suppresses.
	age := now.Sub(s.LastAttempt)
	return age >= 0 && age < Interval, nil
}

func writeState(root string, now time.Time) error {
	// A newly created root is owner-only like other Galley root creators;
	// MkdirAll leaves an existing root's permissions untouched.
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state{LastAttempt: now.UTC()})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, stateFileName), data, 0o600)
}

func fetchLatestTag(opts Options) (string, bool) {
	do := opts.Do
	if do == nil {
		client := &http.Client{Timeout: Timeout}
		do = client.Do
	}
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = Endpoint
	}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false
	}
	var release latestRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return "", false
	}
	return release.TagName, true
}

var stableSemver = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// isNewer reports whether latest is a strictly newer stable release than
// current; build metadata is valid SemVer and ignored for precedence.
func isNewer(current, latest string) bool {
	c, okC := parseStable(current)
	l, okL := parseStable(latest)
	if !okC || !okL {
		return false
	}
	for i := range c {
		if cmp := compareNumeric(l[i], c[i]); cmp != 0 {
			return cmp > 0
		}
	}
	return false
}

func parseStable(v string) ([3]string, bool) {
	var out [3]string
	m := stableSemver.FindStringSubmatch(v)
	if m == nil {
		return out, false
	}
	copy(out[:], m[1:4])
	return out, true
}

// compareNumeric orders validated (leading-zero-free) decimal identifiers
// numerically without fixed-width conversion, so arbitrary-length cores cannot overflow.
func compareNumeric(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}
