package main

import (
	"net/http"
	"net/http/httptest"
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

	handler := routes(srv)
	for _, path := range []string{"/", "/assets/tush.js", "/somewhere/else"} {
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, path, nil))

		if started.Load() {
			t.Fatalf("GET %s started a shell; anything that fetches the URL would open a session", path)
		}
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
	}))

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
