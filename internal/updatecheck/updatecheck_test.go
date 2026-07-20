package updatecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

type fakeTransport struct {
	calls   int
	handler func(*http.Request) (*http.Response, error)
}

func (f *fakeTransport) do(req *http.Request) (*http.Response, error) {
	f.calls++
	return f.handler(req)
}

func releaseResponse(t *testing.T, tag string) *http.Response {
	t.Helper()
	body, err := json.Marshal(latestRelease{TagName: tag})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}
}

func baseOptions(root string, transport *fakeTransport) Options {
	return Options{
		Root:           root,
		Now:            func() time.Time { return testNow },
		IsTTY:          func() bool { return true },
		Do:             transport.do,
		CurrentVersion: "0.12.0",
		Stderr:         &bytes.Buffer{},
	}
}

func readAttemptTime(t *testing.T, root string) time.Time {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, stateFileName))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	return s.LastAttempt
}

func TestNonTTYSkipsRequestAndState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	transport := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
		return releaseResponse(t, "v0.13.0"), nil
	}}
	opts := baseOptions(root, transport)
	opts.IsTTY = func() bool { return false }

	Run(opts)

	if transport.calls != 0 {
		t.Fatalf("non-TTY run made %d requests", transport.calls)
	}
	if _, err := os.Stat(filepath.Join(root, stateFileName)); !os.IsNotExist(err) {
		t.Fatalf("non-TTY run created update state, err=%v", err)
	}
}

func TestStaleOrAbsentRecordChecksOnceThenSuppresses(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	transport := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
		return releaseResponse(t, "v0.13.0"), nil
	}}
	opts := baseOptions(root, transport)

	Run(opts)
	if transport.calls != 1 {
		t.Fatalf("first start made %d requests, want 1", transport.calls)
	}
	if got := readAttemptTime(t, root); !got.Equal(testNow) {
		t.Fatalf("attempt time got %v, want %v", got, testNow)
	}
	stderr := opts.Stderr.(*bytes.Buffer).String()
	for _, want := range []string{"0.12.0", "0.13.0", "update"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("notice missing %q: %q", want, stderr)
		}
	}

	Run(opts)
	if transport.calls != 1 {
		t.Fatalf("fresh record start made %d requests, want 1 total", transport.calls)
	}
}

func TestStaleRecordTriggersNewAttempt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stale, err := json.Marshal(state{LastAttempt: testNow.Add(-25 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, stateFileName), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	transport := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
		return releaseResponse(t, "v0.13.0"), nil
	}}

	Run(baseOptions(root, transport))

	if transport.calls != 1 {
		t.Fatalf("stale record start made %d requests, want 1", transport.calls)
	}
	if got := readAttemptTime(t, root); !got.Equal(testNow) {
		t.Fatalf("attempt time got %v, want %v", got, testNow)
	}
}

func TestRecordAgeBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		age        time.Duration
		wantCalls  int
		wantRecord time.Time
	}{
		{"future record after clock rollback", -time.Hour, 1, testNow},
		{"exactly 24 hours old", Interval, 1, testNow},
		{"just under 24 hours old", Interval - time.Nanosecond, 0, testNow.Add(-(Interval - time.Nanosecond))},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			recorded := testNow.Add(-tc.age)
			data, err := json.Marshal(state{LastAttempt: recorded})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, stateFileName), data, 0o600); err != nil {
				t.Fatal(err)
			}
			transport := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
				return releaseResponse(t, "v0.13.0"), nil
			}}

			Run(baseOptions(root, transport))

			if transport.calls != tc.wantCalls {
				t.Fatalf("age %v: made %d requests, want %d", tc.age, transport.calls, tc.wantCalls)
			}
			if got := readAttemptTime(t, root); !got.Equal(tc.wantRecord) {
				t.Fatalf("age %v: attempt time got %v, want %v", tc.age, got, tc.wantRecord)
			}
		})
	}
}

func TestFailedRequestStillRecordsAttempt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	transport := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	}}
	opts := baseOptions(root, transport)

	Run(opts)

	if transport.calls != 1 {
		t.Fatalf("failing start made %d requests, want 1", transport.calls)
	}
	if got := readAttemptTime(t, root); !got.Equal(testNow) {
		t.Fatalf("attempt time got %v, want %v", got, testNow)
	}
	if stderr := opts.Stderr.(*bytes.Buffer).String(); stderr != "" {
		t.Fatalf("failed request leaked stderr output: %q", stderr)
	}

	Run(opts)
	if transport.calls != 1 {
		t.Fatalf("failed attempt did not rate-limit: %d requests", transport.calls)
	}
}

func TestUnpersistableStateSkipsRequest(t *testing.T) {
	t.Parallel()
	blockingFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
		return releaseResponse(t, "v0.13.0"), nil
	}}
	opts := baseOptions(filepath.Join(blockingFile, "root"), transport)

	Run(opts)

	if transport.calls != 0 {
		t.Fatalf("unpersistable state still made %d requests", transport.calls)
	}
}

func TestHTTPAndResponseFailuresStaySilent(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*http.Request) (*http.Response, error){
		"request error": func(req *http.Request) (*http.Response, error) {
			return nil, errors.New("timeout")
		},
		"non-200 status": func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		},
		"invalid JSON": func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("not json")), Header: make(http.Header)}, nil
		},
	}
	for name, handler := range cases {
		handler := handler
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			transport := &fakeTransport{handler: handler}
			opts := baseOptions(root, transport)

			Run(opts)

			if transport.calls != 1 {
				t.Fatalf("made %d requests, want 1", transport.calls)
			}
			if stderr := opts.Stderr.(*bytes.Buffer).String(); stderr != "" {
				t.Fatalf("failure leaked stderr output: %q", stderr)
			}
			if got := readAttemptTime(t, root); !got.Equal(testNow) {
				t.Fatalf("attempt time got %v, want %v", got, testNow)
			}
		})
	}
}

func TestVersionMatrixNoticeOnlyForNewerStable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		current string
		latest  string
		notice  bool
	}{
		{"newer minor", "0.12.0", "v0.13.0", true},
		{"newer patch", "0.12.0", "0.12.1", true},
		{"newer with build metadata", "0.12.0", "v0.12.1+build.7", true},
		{"equal", "0.12.0", "v0.12.0", false},
		{"equal precedence differing build", "0.12.0", "v0.12.0+build.7", false},
		{"older", "0.12.0", "v0.11.9", false},
		{"malformed latest", "0.12.0", "not-a-version", false},
		{"leading-zero latest", "0.12.0", "v0.013.0", false},
		{"newer overflowing major", "0.12.0", "v18446744073709551616.0.0", true},
		{"equal overflowing major", "18446744073709551616.0.0", "v18446744073709551616.0.0", false},
		{"prerelease latest", "0.12.0", "v0.13.0-rc.1", false},
		{"dev current", "dev", "v0.13.0", false},
		{"empty current", "", "v0.13.0", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			transport := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
				return releaseResponse(t, tc.latest), nil
			}}
			opts := baseOptions(root, transport)
			opts.CurrentVersion = tc.current

			Run(opts)

			stderr := opts.Stderr.(*bytes.Buffer).String()
			if tc.notice && stderr == "" {
				t.Fatalf("expected notice for %s -> %s", tc.current, tc.latest)
			}
			if !tc.notice && stderr != "" {
				t.Fatalf("unexpected notice for %s -> %s: %q", tc.current, tc.latest, stderr)
			}
		})
	}
}

func TestNoticeIsConciseAndMachineRecognizable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	transport := &fakeTransport{handler: func(req *http.Request) (*http.Response, error) {
		return releaseResponse(t, "v0.13.0"), nil
	}}
	var stderr bytes.Buffer
	opts := baseOptions(root, transport)
	opts.Stderr = &stderr

	Run(opts)

	if got, want := stderr.String(), "galley update available: 0.12.0 -> v0.13.0\n"; got != want {
		t.Fatalf("notice got %q, want %q", got, want)
	}
}

func TestIsNewerComparisonBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.2.3", "1.2.4", true},
		{"1.2.3", "1.3.0", true},
		{"1.2.3", "2.0.0", true},
		{"1.2.3", "1.2.3", false},
		{"1.2.3", "1.2.2", false},
		{"1.10.0", "1.9.9", false},
		{"1.2.3", "v1.10.0", true},
		{"1.2.3", "1.2.3+build", false},
		{"1.2.3", "1.2.4+build", true},
		{"1.2.3", "v1.2.3+other.build", false},
		{"1.2.3+build", "1.2.4", true},
		{"1.2.3", "v01.2.4", false},
		{"1.2.3", "1.2.04", false},
		{"01.2.3", "1.2.4", false},
		{"1.2.3", "1.2.3+bad_meta", false},
		{"18446744073709551616.2.3", "18446744073709551617.2.3", true},
		{"18446744073709551616.2.3", "18446744073709551616.2.3", false},
		{"18446744073709551617.2.3", "18446744073709551616.2.3", false},
		{"1.9223372036854775808.3", "1.18446744073709551616.0", true},
		{"1.2.99999999999999999998", "1.2.99999999999999999999", true},
		{"1.2.99999999999999999999", "1.2.99999999999999999998", false},
	}
	for _, tc := range cases {
		if got := isNewer(tc.current, tc.latest); got != tc.want {
			t.Fatalf("isNewer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestDefaultTimeoutBound(t *testing.T) {
	t.Parallel()
	if Timeout > 2*time.Second {
		t.Fatalf("update check timeout %v exceeds the two-second bound", Timeout)
	}
}
