package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/scaffoldly/tush/attach"
	"github.com/scaffoldly/tush/console"
)

// TestPageStartsNoShell is the one that keeps a browser terminal from being a
// worse exposure than a command line one.
//
// The URL is the only credential there is, and things that are not people fetch
// URLs: link unfurlers, chat previews, history sync, crawlers. Fetching the
// page must therefore be inert. The shell starts on the first attach and not
// before, which is what makes pressing Connect — a human act — the thing that
// opens a session.
func TestPageStartsNoShell(t *testing.T) {
	con, err := console.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { con.Close() })

	var started atomic.Bool
	srv := attach.New(con, func(term string) (<-chan int, error) {
		started.Store(true)
		status := make(chan int, 1)
		return status, nil
	})

	handler := routes(srv, func() {})
	for _, path := range []string{"/", "/assets/tush.js", "/somewhere/else"} {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, path, nil))

		if started.Load() {
			t.Fatalf("GET %s started a shell; anything that fetches the URL would open a session", path)
		}
	}
}

// TestInvocationIsRunnable checks that the command tush tells people to run is
// one they could actually run.
//
// It prints a command meant to be typed on another machine, and its own name is
// `tush` however it was reached — which is wrong for both ways of running it
// that install nothing. Somebody who got here through npx has no `tush`, and
// neither does somebody who used `go run`.
func TestInvocationIsRunnable(t *testing.T) {
	goRun := filepath.Join(os.TempDir(), "go-build2751321575", "b001", "exe", "tush")

	for _, tc := range []struct {
		name  string
		env   string
		argv0 string
		want  string
	}{
		{
			name:  "installed on the path",
			argv0: "/usr/local/bin/tush",
			want:  "tush",
		},
		{
			name:  "built into the working directory",
			argv0: "./tush",
			want:  "tush",
		},
		{
			// The binary is in a directory that will not exist shortly, and the
			// reader has no tush of their own.
			name:  "go run",
			argv0: goRun,
			want:  "go run github.com/scaffoldly/tush@latest",
		},
		{
			// What the npm wrapper sets, because npx leaves nothing installed.
			name: "a launcher that knows better",
			env:  "npx @scaffoldly/tush",
			want: "npx @scaffoldly/tush",
		},
		{
			// The value reaches a terminal, and a command hint is no place to
			// find escape sequences.
			name: "escape sequences are stripped",
			env:  "npx \x1b[31m@scaffoldly/tush\x07",
			want: "npx [31m@scaffoldly/tush",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envCommand, tc.env)
			if got := invocation(tc.argv0); got != tc.want {
				t.Errorf("invocation(%q) = %q, want %q", tc.argv0, got, tc.want)
			}
		})
	}
}

// TestModulePathComesFromTheBuild checks the go run hint names the module this
// binary was actually built from. It has been renamed once already, and a copy
// written out by hand would have survived the rename and gone on telling people
// to fetch something that no longer exists.
func TestModulePathComesFromTheBuild(t *testing.T) {
	if got := modulePath(); got != "github.com/scaffoldly/tush" {
		t.Errorf("modulePath() = %q, want the module in go.mod", got)
	}
}

// TestRoutesKeepTheEndpoint checks the page has not swallowed the attach
// endpoint. The page is the catch-all, so a routing mistake would leave every
// client — including tush's own — being handed HTML instead of a session, and
// the endpoint answering a browser rather than the protocol.
func TestRoutesKeepTheEndpoint(t *testing.T) {
	con, err := console.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { con.Close() })

	handler := routes(attach.New(con, func(string) (<-chan int, error) {
		return make(chan int, 1), nil
	}), func() {})

	// A request selecting no streams is refused by the attach endpoint and by
	// nothing else, so a 400 is proof of which handler answered. It is the same
	// signal waitForEdge uses to tell this process from the tunnel's edge.
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, attach.Path, nil))
	if resp.Code != http.StatusBadRequest {
		t.Errorf("GET %s = %d, want %d — the page has taken over the attach endpoint",
			attach.Path, resp.Code, http.StatusBadRequest)
	}

	// And the page is still served beside it.
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/", nil))
	if resp.Code != http.StatusOK {
		t.Errorf("GET / = %d, want %d", resp.Code, http.StatusOK)
	}
}
