package gnome

import (
	"net/url"
	"path"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The pieces a file URI is made of.
const (
	// schemeFile is the only scheme a screenshot result may carry.
	schemeFile = "file://"
	// localAuthority is the host a file URI names when it means this machine.
	// The interface allows it to be empty, which is the usual form.
	localAuthority = "localhost"
)

// resultPath turns the filename GNOME Shell reports into the local path to
// read.
//
// The interface returns a filename rather than a URI, and it is allowed to
// answer with a basename that it resolved into the user's pictures directory. An
// unqualified name is therefore refused: this package always asks for an
// absolute path inside its own temporary directory, so anything else is a
// picture this package did not name and must not read or delete.
//
// A URI is accepted as well because some shells have answered with one, and a
// file URI is the other spelling of the same thing. An empty result means the
// shell kept the name it was given.
func resultPath(used, requested string) (string, error) {
	if used == "" {
		used = requested
	}

	if strings.HasPrefix(used, schemeFile) {
		return filePath(used)
	}

	if !path.IsAbs(used) {
		return "", errs.New(
			errs.CodeInvalidInput,
			"GNOME Shell answered with a filename that is not an absolute path, "+
				"and this package will not guess which file it wrote",
		)
	}

	if strings.ContainsRune(used, 0) {
		return "", errs.New(
			errs.CodeInvalidInput,
			"GNOME Shell answered with a filename containing a NUL byte",
		)
	}

	cleaned := path.Clean(used)
	if cleaned == "/" {
		return "", errs.New(
			errs.CodeInvalidInput,
			"GNOME Shell answered with a filename that names no file",
		)
	}

	return cleaned, nil
}

// filePath turns a file URI into the local path to read.
//
// Only a local URI is accepted: "file:///path" and "file://localhost/path", with
// the percent-encoding a shell applies to a path that contains spaces or
// non-ASCII characters. A URI naming another host is refused rather than read,
// because the path it carries would resolve against this machine and quietly
// name a different file.
//
// The result is cleaned and must stay absolute. Query and fragment are dropped:
// a file URI with either names the same file.
func filePath(uri string) (string, error) {
	rest, isFile := strings.CutPrefix(uri, schemeFile)
	if !isFile {
		return "", errs.Newf(
			errs.CodeInvalidInput,
			"GNOME Shell returned %q, which is not a file URI",
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
				"GNOME Shell returned %q, which names a host other than this machine",
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
			"GNOME Shell returned %q, which is not correctly percent-encoded",
			uri,
		)
	}

	if strings.ContainsRune(decoded, 0) {
		return "", errs.New(
			errs.CodeInvalidInput,
			"GNOME Shell returned a path containing a NUL byte",
		)
	}

	cleaned := path.Clean(decoded)
	if !strings.HasPrefix(cleaned, "/") || cleaned == "/" {
		return "", errs.Newf(
			errs.CodeInvalidInput,
			"GNOME Shell returned %q, which names no file",
			uri,
		)
	}

	return cleaned, nil
}

// insideDir reports whether target names a file inside dir.
//
// The comparison is on cleaned slash-separated paths, which is what the shell
// writes on this platform. A target equal to dir itself is not inside it: the
// shell writes a file, not the directory.
func insideDir(dir, target string) bool {
	dir = path.Clean(dir)
	target = path.Clean(target)

	if target == dir {
		return false
	}

	if dir == "/" {
		return strings.HasPrefix(target, "/")
	}

	return strings.HasPrefix(target, dir+"/")
}
