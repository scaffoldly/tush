// Package web serves a terminal in the browser, so that attaching to a
// published shell needs nothing installed on the far end.
//
// The page speaks the same protocol tush's own client does. It has to: the
// server offers one endpoint and one wire format, and a browser can speak that
// format natively — a WebSocket with a subprotocol, carrying binary frames each
// tagged with a channel. So there is no second protocol here, only a second
// client, written in JavaScript and handed the constants that define the first.
//
// Nothing is attached until a human presses a button. That is the whole reason
// the page and the connection are separate steps: a URL that yields a shell by
// being pasted into a browser is fetched by things that are not people — link
// unfurlers, chat previews, history sync — and none of them may open a session.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/scaffoldly/tush/attach"
	"github.com/scaffoldly/tush/console"
	"github.com/scaffoldly/tush/debug"
)

// terminalType is what the page claims to be. xterm.js implements this, so it
// is what the shell should be told; the terminal client forwards whatever the
// user's own terminal claims instead.
const terminalType = "xterm-256color"

// assetPrefix is where this package's own files are served from.
const assetPrefix = "/assets/"

// iconFile is the page's icon, served both where the page points at it and at
// the fixed path browsers ask for regardless.
const iconFile = "icon.svg"

//go:embed index.html assets
var embedded embed.FS

// Handler serves the page and the small amount of JavaScript that drives it.
// Everything else the page needs comes from a CDN, pinned by hash — see
// assets.go.
//
// stop ends the whole session, tunnel included. It is a capability handed to
// whoever holds the URL, which sounds worse than it is: anyone who can attach
// can already type exit, and that stops the tunnel too. A button only makes it
// obvious.
func Handler(stop func()) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+assetPrefix+"{file}", serveAsset)
	// Explicitly, because otherwise it reaches the catch-all below and a
	// browser asking for an icon is served the whole page — which it then
	// discards, having asked for an image.
	mux.HandleFunc("GET /favicon.ico", serveIcon)
	mux.HandleFunc("POST "+StopPath, stopper(stop))
	mux.HandleFunc("GET /", servePage)
	return mux
}

// StopPath ends the session.
const StopPath = "/stop"

// stopDelay is how long to wait after answering before pulling the session
// down, so that the answer is out of the door first. The teardown closes every
// connection, this one included.
const stopDelay = 250 * time.Millisecond

// stopper ends the session on request.
//
// POST rather than GET, and that is the whole security story here. The URL is
// the credential, so anyone who can reach this could equally attach and type
// exit; what must not happen is a session ending because something that is not
// a person fetched a link. Unfurlers, crawlers and prefetchers issue GETs, and
// a GET route would hand every one of them a kill switch. A GET falls through
// to the page instead.
//
// The Origin check costs a line and turns away a cross-site fetch. It is not
// load-bearing — an attacker who knows the URL does not need another site to
// do this from — but there is no reason to accept one.
func stopper(stop func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			http.Error(w, "cross-origin request", http.StatusForbidden)
			return
		}

		harden(w)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		debug.Logf("stopping at the browser's request")
		go func() {
			time.Sleep(stopDelay)
			stop()
		}()
	}
}

func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host == host
}

// serveIcon answers the request a browser makes on its own initiative, whatever
// the page says its icon is.
func serveIcon(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("file", iconFile)
	serveAsset(w, r)
}

// servePage renders the page. Any path that is not an asset gets it, because a
// browser that has landed on a stray path under this URL wants a terminal, not
// a 404 explaining that it took a wrong turn.
func servePage(w http.ResponseWriter, r *http.Request) {
	page, err := render()
	if err != nil {
		debug.Logf("rendering the page failed: %v", err)
		http.Error(w, "the page could not be rendered", http.StatusInternalServerError)
		return
	}

	harden(w)
	// A page that is a capability must not sit in a cache somebody else reads.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// serveAsset serves this package's own files. Only the files it ships exist;
// anything else is a 404 rather than a path to go looking for on disk.
func serveAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	body, err := read(path.Join("assets", name))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	harden(w)
	// Static and carrying nothing about the session, so unlike the page it is
	// worth caching.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Type", contentType(name))
	w.Write(body)
}

// contentType names the type explicitly rather than leaving it to be sniffed
// or looked up in a system mime table, which differs between machines.
func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}

// harden sets the headers that matter on a page whose URL is the only
// credential there is.
//
// None of this is authentication — that boundary belongs to the tunnel
// provider. It is about the ways a browser leaks a URL that a command line
// never does: to a referrer, to a shared cache, to a search index.
func harden(w http.ResponseWriter) {
	h := w.Header()

	// The URL is the capability. Any request that carried a Referer would hand
	// it to whoever received it — which now includes the CDN.
	h.Set("Referrer-Policy", "no-referrer")

	// A crawler that indexes this URL publishes the shell behind it.
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")

	// A shell in an iframe is clickjacking bait. frame-ancestors below is the
	// modern form; this is for agents that predate it.
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")

	h.Set("Content-Security-Policy", policy())
}

// policy is the content security policy. Script and style come from this binary
// or from the pinned CDN, and nothing else — in particular connect-src is this
// origin alone, so nothing the page loads can carry the session anywhere.
//
// 'unsafe-inline' covers styles only, and is unavoidable: xterm.js sizes itself
// by writing inline styles. No inline script exists, which is why the page
// hands its configuration to JavaScript as JSON in a non-executable block
// rather than as a script tag.
func policy() string {
	return strings.Join([]string{
		"default-src 'none'",
		"script-src 'self' " + origins(),
		"style-src 'self' 'unsafe-inline' " + origins(),
		"img-src 'self' data:",
		"font-src 'self' " + origins(),
		"connect-src 'self'",
		"base-uri 'none'",
		"form-action 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}

// page is everything index.html needs.
type page struct {
	XtermJS  asset
	XtermCSS asset
	XtermFit asset
	Config   template.JS
}

// config is what the page needs in order to speak the protocol. Every field
// comes from a Go constant that already defines the server's behaviour, so the
// JavaScript cannot drift from it: get one of these wrong by hand and the
// symptom is a terminal that shows nothing, with no error anywhere.
type config struct {
	Endpoint         string   `json:"endpoint"`
	Subprotocol      string   `json:"subprotocol"`
	Channels         channels `json:"channels"`
	KeepAliveSeconds int      `json:"keepAliveSeconds"`
	Busy             string   `json:"busy"`
	Detach           detach   `json:"detach"`
	Stop             string   `json:"stop"`
}

// detach is the key sequence that leaves a session running, as the terminal
// client takes it, so that both clients take the same one.
type detach struct {
	Prefix int `json:"prefix"`
	Suffix int `json:"suffix"`
}

type channels struct {
	Stdin  int `json:"stdin"`
	Stdout int `json:"stdout"`
	Stderr int `json:"stderr"`
	Error  int `json:"error"`
	Resize int `json:"resize"`
}

func settings() config {
	q := url.Values{}
	q.Set(attach.ParamStdin, "1")
	q.Set(attach.ParamStdout, "1")
	q.Set(attach.ParamTTY, "1")
	q.Set(attach.ParamTerm, terminalType)

	return config{
		// A path and a query only. The browser resolves the scheme and host
		// from where it loaded the page, which is the tunnel.
		Endpoint:    attach.Path + "?" + q.Encode(),
		Subprotocol: attach.Subprotocol,
		Channels: channels{
			Stdin:  attach.ChannelStdin,
			Stdout: attach.ChannelStdout,
			Stderr: attach.ChannelStderr,
			Error:  attach.ChannelError,
			Resize: attach.ChannelResize,
		},
		KeepAliveSeconds: int(attach.KeepAlive.Seconds()),
		// So the page can tell "somebody else is attached" — worth retrying —
		// from a failure that is not.
		Busy:   console.ErrBusy.Error(),
		Detach: detach{Prefix: attach.DetachPrefix, Suffix: attach.DetachSuffix},
		Stop:   StopPath,
	}
}

// render builds the page. Nothing in it derives from the request: the
// configuration is Go constants and the asset tags are compile-time values, so
// there is no request-shaped data to escape.
func render() ([]byte, error) {
	tmpl, err := parse()
	if err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(settings())
	if err != nil {
		return nil, err
	}

	var out strings.Builder
	err = tmpl.Execute(&out, page{
		XtermJS:  xtermJS,
		XtermCSS: xtermCSS,
		XtermFit: xtermFit,
		Config:   template.JS(encoded),
	})
	if err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

var (
	parseOnce sync.Once
	parsed    *template.Template
	parseErr  error
)

// parse compiles index.html, once — unless it is being read from a directory,
// where the point is to edit the file and refresh the browser, so it is
// recompiled every time.
func parse() (*template.Template, error) {
	if debug.WebDir() != "" {
		return compile()
	}
	parseOnce.Do(func() { parsed, parseErr = compile() })
	return parsed, parseErr
}

func compile() (*template.Template, error) {
	body, err := read("index.html")
	if err != nil {
		return nil, err
	}
	return template.New("index.html").Parse(string(body))
}

// read returns one of this package's files: from the copy built into the binary
// normally, and from a directory when one has been named.
//
// The directory is opened as an os.Root, which cannot be escaped by a name
// containing .. or by a symlink pointing out of it. The names reaching here
// come from a URL, so making that impossible by construction beats checking the
// string carefully.
func read(name string) ([]byte, error) {
	dir := debug.WebDir()
	if dir == "" {
		return fs.ReadFile(embedded, name)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("serving the page from %s: %w", dir, err)
	}
	defer root.Close()

	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
