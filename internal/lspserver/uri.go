package lspserver

import (
	"fmt"
	"net/url"
	"strings"
)

// uriToPath converts a file:// URI to a filesystem path. Only POSIX-style
// paths are handled: a Windows drive-letter URI ("file:///C:/x") comes back
// as "/C:/x", and pathToURI doesn't convert separators either, so the
// server isn't usable on Windows as it stands.
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

// isFileURI reports whether uri names something on the local filesystem.
//
// Clients send plenty of documents that don't: VS Code's diff view opens
// "git:/path/to/top.sv?{...}" for the indexed side of a change, and an
// unsaved buffer is "untitled:Untitled-1". Those belong in the document
// store (so hover and completion work inside that buffer) but not in the
// workspace index -- indexing one puts a second copy of every declaration
// in the file under a URI nothing can resolve back to a path, so
// goto-definition offers both and rename emits a TextEdit into a document
// the client may not even let the user write.
//
// It agrees with uriToPath on what counts: the scheme, compared
// case-insensitively the way url.Parse normalizes it, is "file". This runs
// for every URI that is opened, changed or published to, so it compares the
// scheme directly rather than parsing the whole URI.
func isFileURI(uri string) bool {
	scheme, _, ok := strings.Cut(uri, ":")
	return ok && strings.EqualFold(scheme, "file")
}
