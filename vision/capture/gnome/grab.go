//go:build linux

package gnome

import (
	"context"
	"os"

	"github.com/LouYuanbo1/neru-core/errs"
)

// grab runs one screenshot request and returns the frame it produced: the
// pixels decoded from the PNG the shell wrote, placed on the layout the
// compositor reports.
//
// The order matters. The layout probe starts first because it is the part that
// talks to the compositor and the screenshot is the part that waits for a render
// and a PNG encode; overlapping them means the capture pays for the layout only
// when the compositor answers more slowly than the shell does. The interface is
// checked before the request so a shell that does not export Screenshot is
// reported as unsupported instead of being asked a question it cannot answer.
func grab(ctx context.Context) (Frame, error) {
	ctx, cancel := withBudget(ctx)
	defer cancel()

	probed := startDesktop()

	conn, err := dialSessionBus(ctx)
	if err != nil {
		return Frame{}, err
	}
	defer conn.Close()

	if err := requireScreenshot(ctx, conn); err != nil {
		return Frame{}, err
	}

	dir, filename, err := tempTarget()
	if err != nil {
		return Frame{}, err
	}
	defer os.RemoveAll(dir)

	used, err := callScreenshot(ctx, conn, filename)
	if err != nil {
		return Frame{}, err
	}

	resolved, err := resultPath(used, filename)
	if err != nil {
		return Frame{}, err
	}

	// The shell is supposed to write the file this package named. If it wrote
	// somewhere else, that file is not this process's to read or delete, so it is
	// left alone and the capture is reported as failed rather than silently
	// returning pixels of an unknown part of the desktop.
	if !insideDir(dir, resolved) {
		return Frame{}, errs.New(
			errs.CodeInternal,
			"GNOME Shell wrote the screenshot somewhere other than the temporary "+
				"file this package named, so it was left alone instead of read",
		)
	}

	img, err := decodePNG(resolved)
	if err != nil {
		return Frame{}, err
	}

	// The picture has been decoded, so the copy on disk is no longer needed; the
	// deferred removal of the directory is the backstop for every path that did
	// not get this far.
	_ = os.Remove(resolved)

	// Decoding a 4K PNG takes long enough that the caller may have given up
	// meanwhile, and returning pixels nobody waits for would report a capture
	// that the caller already counts as canceled.
	if err := checkContext(ctx); err != nil {
		return Frame{}, err
	}

	return placeDesktop(img, <-probed), nil
}

// tempTarget makes the private directory and file name the shell is asked to
// write into.
//
// A directory per capture, created with the process's own permissions, is what
// makes the name unique and keeps the file out of a shared directory where
// another user could plant a symlink at the name this package is about to read.
// The file itself is created empty so the name is reserved; the shell replaces
// its contents when it writes the PNG.
func tempTarget() (string, string, error) {
	dir, err := os.MkdirTemp("", "neru-core-screenshot-")
	if err != nil {
		return "", "", errs.Wrap(
			err,
			errs.CodeInternal,
			"could not create a temporary directory for GNOME Shell's screenshot",
		)
	}

	file, err := os.CreateTemp(dir, "screenshot-*.png")
	if err != nil {
		_ = os.RemoveAll(dir)

		return "", "", errs.Wrap(
			err,
			errs.CodeInternal,
			"could not create the temporary file GNOME Shell writes a screenshot into",
		)
	}

	name := file.Name()

	if err := file.Close(); err != nil {
		_ = os.RemoveAll(dir)

		return "", "", errs.Wrap(
			err,
			errs.CodeInternal,
			"could not close the temporary file GNOME Shell writes a screenshot into",
		)
	}

	return dir, name, nil
}
