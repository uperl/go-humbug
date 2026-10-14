// Package offline serves a fixed set of official schema documents compiled into the binary, so a
// specification that references one of them by its canonical URI loads without network access.
// Nothing else is ever fetched: every other remote URI is refused.
//
// The documents live under schemas/, at the canonical URI's host and path plus ".json"
// (https://spec.openapis.org/oas/3.1/dialect/base is schemas/spec.openapis.org/oas/3.1/dialect/base.json).
// Each one's $id is that URI; a test checks it. They are copied byte for byte from the URIs they
// are named after, under the license in schemas-LICENSE (also in THIRD_PARTY_LICENSES).
package offline

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

//go:embed schemas
var files embed.FS

// resources maps each canonical URI to its document.
var resources = func() map[string][]byte {
	m := map[string][]byte{}
	err := fs.WalkDir(files, "schemas", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		m["https://"+strings.TrimSuffix(strings.TrimPrefix(path, "schemas/"), ".json")] = b
		return nil
	})
	if err != nil {
		panic(err)
	}
	return m
}()

// NotBundledError reports a remote URI that is not one of the bundled documents.
type NotBundledError struct{ URI string }

func (e *NotBundledError) Error() string {
	return fmt.Sprintf("remote reference %q is not available: Humbug does not fetch remote references", e.URI)
}

// Lookup returns the bundled document for uri. The match is exact, apart from an empty fragment
// ("...#"): another scheme, host or path is a different URI and is not bundled.
func Lookup(uri string) ([]byte, bool) {
	b, ok := resources[strings.TrimSuffix(uri, "#")]
	return b, ok
}

// URIs returns the canonical URIs of the bundled documents, sorted.
func URIs() []string {
	u := make([]string, 0, len(resources))
	for k := range resources {
		u = append(u, k)
	}
	sort.Strings(u)
	return u
}

// Fetch answers a GET for uri from the bundle, without network access. It returns a
// *NotBundledError for any other URI.
func Fetch(uri string) (*http.Response, error) {
	b, ok := Lookup(uri)
	if !ok {
		return nil, &NotBundledError{URI: uri}
	}
	return &http.Response{
		Status: "200 OK", StatusCode: http.StatusOK,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{"Content-Type": {"application/schema+json"}},
		Body:          io.NopCloser(bytes.NewReader(b)),
		ContentLength: int64(len(b)),
	}, nil
}

// Transport is an http.RoundTripper that answers GETs from the bundle and refuses every other
// request, so code that uses it can never reach the network.
type Transport struct{}

func (Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		r.Body.Close()
	}
	if r.Method != http.MethodGet {
		return nil, fmt.Errorf("%s %s: Humbug makes no network requests", r.Method, r.URL)
	}
	res, err := Fetch(r.URL.String())
	if err != nil {
		return nil, err
	}
	res.Request = r
	return res, nil
}
