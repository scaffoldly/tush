package web

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestAssetHashesStillMatch fetches each pinned asset and checks it hashes to
// what assets.go records.
//
// This is the same check a browser makes on every load, done deliberately. A
// mismatch means one of two things and both want a human: a version was bumped
// without recording the new hash, or the CDN is serving something other than
// what was published — which, on a page that hands out a shell, is the failure
// this whole arrangement exists to prevent.
//
// It reaches the network, so it is skipped unless TUSH_SRI is set, the way the
// live tests are. It also logs the hash of what it found, which is how the
// value to record after a version bump is produced.
func TestAssetHashesStillMatch(t *testing.T) {
	if os.Getenv("TUSH_SRI") == "" {
		t.Skip("set TUSH_SRI to check the pinned assets against the CDN")
	}

	for _, a := range assets {
		t.Run(a.URL, func(t *testing.T) {
			got, err := fetchHash(t, a.URL)
			if err != nil {
				t.Fatalf("could not fetch %s: %v", a.URL, err)
			}

			t.Logf("%s\n  %s", a.URL, got)
			if got != a.Integrity {
				t.Errorf("hash has changed under a pinned version\n  recorded %s\n  served   %s\n"+
					"Either the version was bumped without recording the new hash, or the CDN "+
					"is serving bytes that were not published.", a.Integrity, got)
			}
		})
	}
}

func fetchHash(t *testing.T, url string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	sum := sha512.New384()
	if _, err := io.Copy(sum, resp.Body); err != nil {
		return "", err
	}
	// The form the integrity attribute takes: the algorithm, then the digest in
	// standard base64.
	return "sha384-" + base64.StdEncoding.EncodeToString(sum.Sum(nil)), nil
}
