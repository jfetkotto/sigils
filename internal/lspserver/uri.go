package lspserver

import (
	"fmt"
	"net/url"
)

func uriToPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported URI scheme %q", u.Scheme)
	}
	return u.Path, nil
}

// pathToURI is uriToPath's inverse. It's used to key the declaration index
// for files discovered on disk (via a Discoverer) with the same URI form
// an editor would report for that file through didOpen, so a live edit
// correctly overrides what a background scan found rather than creating a
// second, stale entry under a different key.
func pathToURI(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	return u.String()
}
