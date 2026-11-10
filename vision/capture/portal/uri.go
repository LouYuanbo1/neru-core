//go:build linux

package portal

import (
	"net/url"
	"path"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The pieces a file URI is made of.
const (
	// schemeFile is the only scheme a portal screenshot result may carry.
	schemeFile = "file://"
	// localAuthority is the host a file URI names when it means this machine.
	// The interface allows it to be empty, which is the usual form.
	localAuthority = "localhost"
)

// FilePath turns the file URI in a portal result into the local path to read.
//
// Only a local URI is accepted: "file:///path" and "file://localhost/path",
// with the percent-encoding a portal applies to a path that contains spaces or
// non-ASCII characters, which is the form a document-portal mount produces when
// the name came from the user. A URI naming another host is refused rather than
// read, because the path it carries would resolve against this machine and
// quietly name a different file.
//
// The result is cleaned and must stay absolute. Query and fragment are dropped:
// a file URI with either names the same file.
func FilePath(uri string) (string, error) {
	rest, isFile := strings.CutPrefix(uri, schemeFile)
	if !isFile {
		return "", errs.Newf(
			errs.CodeInvalidInput,
			"the screenshot portal returned %q, which is not a file URI",
			uri,
		)
	}

	target := rest

	// Anything before the first slash is the authority, and an empty authority
	// (file:///path) is this machine.
	if !strings.HasPrefix(rest, "/") {
		authority, tail, _ := strings.Cut(rest, "/")
		if !strings.EqualFold(authority, localAuthority) {
			return "", errs.Newf(
				errs.CodeInvalidInput,
				"the screenshot portal returned %q, which names a host other than this machine",
				uri,
			)
		}

		target = "/" + tail
	}

	if cut := strings.IndexAny(target, "?#"); cut >= 0 {
		target = target[:cut]
	}

	decoded, err := url.PathUnescape(target)
	if err != nil {
		return "", errs.Wrapf(
			err,
			errs.CodeInvalidInput,
			"the screenshot portal returned %q, which is not correctly percent-encoded",
			uri,
		)
	}

	if strings.ContainsRune(decoded, 0) {
		return "", errs.New(
			errs.CodeInvalidInput,
			"the screenshot portal returned a path containing a NUL byte",
		)
	}

	cleaned := path.Clean(decoded)
	if !strings.HasPrefix(cleaned, "/") || cleaned == "/" {
		return "", errs.Newf(
			errs.CodeInvalidInput,
			"the screenshot portal returned %q, which names no file",
			uri,
		)
	}

	return cleaned, nil
}
