package lspserver

import "testing"

// isFileURI must accept exactly the URIs uriToPath turns into a path.
func TestIsFileURIAgreesWithURIToPath(t *testing.T) {
	for _, uri := range []string{
		"file:///top.sv",
		"FILE:///top.sv",
		"File:///dir/top.sv",
		"file:/top.sv",
		"git:/top.sv?{}",
		"untitled:Untitled-1",
		"filex:///top.sv",
		"/no/scheme.sv",
		"",
	} {
		_, err := uriToPath(uri)
		if got, want := isFileURI(uri), err == nil; got != want {
			t.Errorf("isFileURI(%q) = %v, but uriToPath error = %v", uri, got, err)
		}
	}
}
