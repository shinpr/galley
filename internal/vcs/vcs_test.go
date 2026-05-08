package vcs

import "testing"

func TestDecodePRCommentsSlurpPages(t *testing.T) {
	t.Parallel()
	comments, err := DecodePRComments(`[[{"id":1,"body":"first"}],[{"id":2,"body":"second"}]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[0].ID != 1 || comments[1].ID != 2 {
		t.Fatalf("comments got %#v", comments)
	}
}

func TestParseGitHubPRURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "valid", raw: "https://github.com/example/galley/pull/123"},
		{name: "missing pull", raw: "https://github.com/example/galley/issues/123", wantErr: true},
		{name: "short", raw: "https://github.com/example/galley", wantErr: true},
		{name: "wrong host", raw: "https://attacker.example/example/galley/pull/123", wantErr: true},
		{name: "non numeric", raw: "https://github.com/example/galley/pull/abc", wantErr: true},
		{name: "invalid url", raw: "://bad", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := ParseGitHubPRURL(tt.raw)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestExtractFirstHTTPSURL(t *testing.T) {
	t.Parallel()
	stdout := "warning: something happened\nhttps://github.com/example/repo/pull/123\n"
	url := ExtractFirstHTTPSURL(stdout)
	if url != "https://github.com/example/repo/pull/123" {
		t.Fatalf("url got %q", url)
	}
}
