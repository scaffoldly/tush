package web

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/scaffoldly/tush/attach"
)

// TestPageServes checks the page is there at all, and that it is not left
// anywhere a cache would keep it: the URL is the only credential, so a
// capability page sitting in a shared cache is a session handed to whoever
// reads that cache.
func TestPageServes(t *testing.T) {
	resp := get(t, "/")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got, want := resp.Header().Get("Content-Type"), "text/html; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// TestStrayPathsGetThePage checks a browser that landed under some other path
// still gets a terminal rather than a 404 telling it that it took a wrong turn.
func TestStrayPathsGetThePage(t *testing.T) {
	if resp := get(t, "/somewhere/else"); resp.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.Code, http.StatusOK)
	}
}

// TestHardeningHeaders checks each header that exists because a browser leaks a
// URL in ways a command line does not. None of these is authentication — that
// boundary belongs to the tunnel provider — and each one is here because
// without it the URL escapes somewhere.
func TestHardeningHeaders(t *testing.T) {
	resp := get(t, "/")

	for header, want := range map[string]string{
		// The URL is the capability, and the page now talks to a third-party
		// origin, so the default policy would hand it over in a Referer.
		"Referrer-Policy": "no-referrer",
		// A crawler that indexes this URL publishes the shell.
		"X-Robots-Tag": "noindex, nofollow, noarchive",
		// A shell in an iframe is clickjacking bait.
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := resp.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

// TestPolicyConfinesThePage checks the content security policy allows script
// and style from this binary and the pinned CDN, and nothing else — and in
// particular that connect-src names no other host, so that nothing the page
// loads can carry the session somewhere.
func TestPolicyConfinesThePage(t *testing.T) {
	directives := map[string]string{}
	for _, d := range strings.Split(get(t, "/").Header().Get("Content-Security-Policy"), ";") {
		fields := strings.Fields(d)
		if len(fields) > 0 {
			directives[fields[0]] = strings.Join(fields[1:], " ")
		}
	}

	if got, want := directives["default-src"], "'none'"; got != want {
		t.Errorf("default-src = %q, want %q", got, want)
	}
	if got, want := directives["connect-src"], "'self'"; got != want {
		t.Errorf("connect-src = %q, want %q — the page must not be able to reach any other host", got, want)
	}
	if got, want := directives["frame-ancestors"], "'none'"; got != want {
		t.Errorf("frame-ancestors = %q, want %q", got, want)
	}

	// The CDN has to be an allowed origin for the assets to load at all. That
	// is exactly why the integrity hashes matter: naming the origin permits
	// anything it serves, and only the hash narrows that to the bytes meant.
	for _, directive := range []string{"script-src", "style-src"} {
		if !strings.Contains(directives[directive], origins()) {
			t.Errorf("%s = %q, missing the asset origin %q", directive, directives[directive], origins())
		}
	}
	if strings.Contains(directives["script-src"], "'unsafe-inline'") {
		t.Error("script-src allows inline script; the page is built so that none exists")
	}
}

// TestRemoteTagsArePinned is the one that guards the CDN.
//
// Three things have to hold on every tag, and none of them fails visibly: a
// missing integrity means the CDN is trusted outright, a missing crossorigin
// means the browser cannot hash the response and skips the check without
// saying so, and a version range means the bytes may change under the hash.
// The rendered HTML is checked rather than the constants behind it, because
// what protects the user is what reaches the browser.
func TestRemoteTagsArePinned(t *testing.T) {
	body := get(t, "/").Body.String()

	for _, a := range assets {
		// Decoded the way a browser decodes an attribute value. html/template
		// escapes the + in a base64 hash to &#43;, so the rendered source does
		// not contain the hash literally even though what the browser ends up
		// comparing against does.
		tag := html.UnescapeString(tagFor(t, body, a.URL))

		if !strings.Contains(tag, `integrity="`+a.Integrity+`"`) {
			t.Errorf("tag for %s carries no integrity hash:\n%s", a.URL, tag)
		}
		if !strings.HasPrefix(a.Integrity, "sha384-") {
			t.Errorf("integrity for %s is %q, want a sha384 hash", a.URL, a.Integrity)
		}
		if !strings.Contains(tag, `crossorigin="anonymous"`) {
			t.Errorf("tag for %s has no crossorigin, so the browser will skip the integrity check:\n%s", a.URL, tag)
		}
		if !pinned(a.URL) {
			t.Errorf("%s is not pinned to an exact version; the bytes can change under the hash", a.URL)
		}
	}
}

// pinned reports whether a jsdelivr-style URL names one exact version rather
// than a range that resolves to whatever is newest.
var exactVersion = regexp.MustCompile(`@\d+\.\d+\.\d+/`)

func pinned(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return exactVersion.MatchString(u.Path)
}

// tagFor returns the whole element that loads the given URL.
func tagFor(t *testing.T, body, target string) string {
	t.Helper()
	at := strings.Index(body, target)
	if at < 0 {
		t.Fatalf("nothing in the page loads %s", target)
	}
	start := strings.LastIndex(body[:at], "<")
	end := strings.Index(body[at:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("could not find the tag around %s", target)
	}
	return body[start : at+end+1]
}

// TestConfigMatchesTheProtocol checks the page is handed the real protocol
// rather than a copy of it. Every value here is a Go constant that already
// decides how the server behaves, and a JavaScript copy that drifted from one
// would show an empty terminal with no error anywhere — which is why they are
// rendered rather than written twice.
func TestConfigMatchesTheProtocol(t *testing.T) {
	var got config
	if err := json.Unmarshal([]byte(configJSON(t)), &got); err != nil {
		t.Fatalf("the page's configuration is not valid JSON: %v", err)
	}

	endpoint, err := url.Parse(got.Endpoint)
	if err != nil {
		t.Fatalf("endpoint %q does not parse: %v", got.Endpoint, err)
	}
	if endpoint.Path != attach.Path {
		t.Errorf("endpoint path = %q, want %q", endpoint.Path, attach.Path)
	}
	if endpoint.IsAbs() || endpoint.Host != "" {
		t.Errorf("endpoint = %q, want a path and query only so the browser resolves the tunnel it loaded from", got.Endpoint)
	}

	// "1", never "true": remotecommand compares against the literal, and the
	// 400 it returns otherwise reads like a missing parameter.
	for _, param := range []string{attach.ParamStdin, attach.ParamStdout, attach.ParamTTY} {
		if v := endpoint.Query().Get(param); v != "1" {
			t.Errorf("query %s = %q, want %q", param, v, "1")
		}
	}
	if v := endpoint.Query().Get(attach.ParamTerm); v != terminalType {
		t.Errorf("query %s = %q, want %q", attach.ParamTerm, v, terminalType)
	}

	if got.Subprotocol != attach.Subprotocol {
		t.Errorf("subprotocol = %q, want %q", got.Subprotocol, attach.Subprotocol)
	}
	want := channels{
		Stdin:  attach.ChannelStdin,
		Stdout: attach.ChannelStdout,
		Stderr: attach.ChannelStderr,
		Error:  attach.ChannelError,
		Resize: attach.ChannelResize,
	}
	if got.Channels != want {
		t.Errorf("channels = %+v, want %+v", got.Channels, want)
	}
	if got.KeepAliveSeconds != int(attach.KeepAlive.Seconds()) {
		t.Errorf("keepAliveSeconds = %d, want %d", got.KeepAliveSeconds, int(attach.KeepAlive.Seconds()))
	}
	if got.Busy == "" {
		t.Error("the page cannot tell a busy console from any other failure, so it will not retry a refresh")
	}
}

// configJSON returns the configuration block as the browser would read it.
//
// It goes through the same JSON.parse the page does, in effect, which is the
// point: html/template escapes by context, and a block that came back
// HTML-escaped would still look right in the source while parsing to something
// else — an endpoint whose & had become &amp; would silently lose its query.
func configJSON(t *testing.T) string {
	t.Helper()
	body := get(t, "/").Body.String()

	const marker = `id="tush-config">`
	at := strings.Index(body, marker)
	if at < 0 {
		t.Fatal("the page carries no configuration block")
	}
	rest := body[at+len(marker):]
	end := strings.Index(rest, "</script>")
	if end < 0 {
		t.Fatal("the configuration block is not closed")
	}
	return rest[:end]
}

// TestGlueServes checks the page's own JavaScript is served, and served as
// JavaScript: the type is named outright rather than left to a system mime
// table that differs between machines.
func TestGlueServes(t *testing.T) {
	resp := get(t, "/assets/tush.js")

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if got, want := resp.Header().Get("Content-Type"), "text/javascript; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if !strings.Contains(resp.Body.String(), "tush-config") {
		t.Error("the glue served does not read the page's configuration")
	}
}

// TestUnknownAssetIsNotFound checks an asset that does not exist is a 404
// rather than something to go looking for.
func TestUnknownAssetIsNotFound(t *testing.T) {
	if resp := get(t, "/assets/nothing.js"); resp.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.Code, http.StatusNotFound)
	}
}

// TestAssetsCannotEscape checks that no name reaching the asset route can serve
// a file from outside the directory it is meant to.
//
// The names come from a URL, so this is checked by what comes back rather than
// by what the code appears to do. Two layers already make it hard — ServeMux
// cleans a literal ../ away before the handler sees it, and an encoded one
// meets an fs that rejects the path — and the assertion here is the property
// itself, which holds however the request is spelled.
func TestAssetsCannotEscape(t *testing.T) {
	for _, name := range []string{
		"../../main.go",
		"..%2f..%2fmain.go",
		"..%252f..%252fmain.go",
		"....//....//main.go",
		"/etc/passwd",
	} {
		body := get(t, "/assets/"+name).Body.String()
		if strings.Contains(body, "package main") || strings.Contains(body, "root:") {
			t.Errorf("GET /assets/%s served a file from outside the assets directory:\n%s", name, body)
		}
	}
}

// TestPageMentionsNoPreview checks the page gives an unfurler nothing worth
// putting in a preview. The URL is the credential, and a rich preview carries
// it further than the person who pasted it intended.
func TestPageMentionsNoPreview(t *testing.T) {
	body := get(t, "/")

	if strings.Contains(body.Body.String(), "og:") {
		t.Error("the page carries Open Graph tags, which is an invitation to unfurl it")
	}
	if !strings.Contains(body.Body.String(), `name="robots"`) {
		t.Error("the page has no robots meta tag; a saved copy keeps the tag and loses the header")
	}
}

// TestIconIsInline checks the page carries its own icon rather than sending the
// browser to fetch one.
//
// Without it a browser asks for /favicon.ico, which the page is the catch-all
// for, so every load fetched the whole page a second time to throw it away. The
// check for ZgotmplZ is the one that matters: html/template refuses a data: URI
// in an href it interpolates and substitutes that marker instead, so writing
// the icon as a rendered value rather than a literal would remove it silently.
// TestNoIconRequestRendersThePage checks that neither way a browser can ask for
// an icon costs it the whole page.
//
// The page is the catch-all, so /favicon.ico reached it and was answered with
// every byte of HTML — for a request that wanted an image and discarded the
// result. That is the cost worth avoiding; the request itself is a few hundred
// cached bytes and does not matter.
//
// An earlier attempt inlined the icon as a data: URI to avoid the request
// altogether, and browsers resolved it as a relative path — so they asked for a
// mangled path and then for /favicon.ico regardless, and it cost two requests
// rather than none. Hence a plain file, and this test on both paths.
func TestNoIconRequestRendersThePage(t *testing.T) {
	page := get(t, "/").Body.String()
	if !strings.Contains(page, `href="/assets/`+iconFile+`"`) {
		t.Errorf("the page does not point at /assets/%s as its icon", iconFile)
	}

	for _, path := range []string{"/favicon.ico", "/assets/" + iconFile} {
		resp := get(t, path)

		if resp.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want %d", path, resp.Code, http.StatusOK)
			continue
		}
		if got, want := resp.Header().Get("Content-Type"), "image/svg+xml"; got != want {
			t.Errorf("GET %s Content-Type = %q, want %q", path, got, want)
		}
		if strings.Contains(resp.Body.String(), "tush-config") {
			t.Errorf("GET %s was answered with the whole page instead of an icon", path)
		}
	}
}

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	resp := httptest.NewRecorder()
	Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, path, nil))
	return resp
}
