//go:build linux

package kwin

import (
	"context"
	"image"
	"io"
	"os"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/errs"
)

// grabScreen runs one whole-output capture and returns the frame it produced.
//
// The layout probe starts first because it is the part that talks to the
// compositor and the D-Bus exchange is the part that can wait; overlapping them
// means the capture pays for the layout only when the compositor answers more
// slowly than KWin does. The interface is read from introspection before the
// call so a KWin that cannot name its active output is refused with a reason
// instead of being asked for the wrong screen.
func grabScreen(ctx context.Context) (Frame, error) {
	ctx, cancel := withBudget(ctx)
	defer cancel()

	probed := startDesktop()

	conn, err := dialSessionBus(ctx)
	if err != nil {
		return Frame{}, err
	}
	defer conn.Close()

	service, capabilities, err := screenShotService(ctx, conn)
	if err != nil {
		return Frame{}, err
	}

	method, ok := capabilities.screenCaptureMethod()
	if !ok {
		return Frame{}, errs.Newf(
			errs.CodeNotSupported,
			"KWin's %s interface at %s exports neither %s nor %s, and this package "+
				"will not call %s because it addresses an output by a name or index "+
				"it cannot resolve",
			screenShotInterface,
			screenShotObjectPath,
			methodCaptureActiveScreen,
			methodCaptureWorkspace,
			methodCaptureScreen,
		)
	}

	meta, img, err := capturePipe(ctx, conn, service, method)
	if err != nil {
		return Frame{}, err
	}

	// Reading the pixels takes long enough that the caller may have given up
	// meanwhile, and returning a frame nobody waits for would report a capture
	// the caller already counts as canceled.
	if err := checkContext(ctx); err != nil {
		return Frame{}, err
	}

	return placeScreen(img, meta, <-probed), nil
}

// grabRegion runs one area capture and returns the frame it produced.
//
// The region is clipped to the compositor's layout when the layout probe found
// one, so a rectangle that hangs off the desktop asks KWin only for the part
// that exists. Without a layout the region is passed through: KWin renders the
// rectangle wherever it is and fills the part with no screen, which is the best
// this package can do when it cannot read the desktop it is capturing.
func grabRegion(ctx context.Context, region image.Rectangle) (Frame, error) {
	ctx, cancel := withBudget(ctx)
	defer cancel()

	probed := startDesktop()

	conn, err := dialSessionBus(ctx)
	if err != nil {
		return Frame{}, err
	}
	defer conn.Close()

	service, capabilities, err := screenShotService(ctx, conn)
	if err != nil {
		return Frame{}, err
	}

	if !capabilities.areaCapture() {
		return Frame{}, errs.Newf(
			errs.CodeNotSupported,
			"KWin's %s interface at %s does not export %s(i, i, u, u, a{sv}, h), "+
				"so a region cannot be captured on this KWin",
			screenShotInterface,
			screenShotObjectPath,
			methodCaptureArea,
		)
	}

	if desktop := <-probed; desktop.known {
		clipped := region.Intersect(desktop.bounds)
		if clipped.Empty() {
			return Frame{}, errs.Newf(
				errs.CodeInvalidInput,
				"region %v lies outside the captured desktop %v",
				region,
				desktop.bounds,
			)
		}

		region = clipped
	}

	// The layout probe can consume the caller's budget on a wedged compositor;
	// asking KWin for a frame nobody is waiting for would only fail later.
	if err := checkContext(ctx); err != nil {
		return Frame{}, err
	}

	meta, img, err := capturePipe(
		ctx,
		conn,
		service,
		methodCaptureArea,
		int32(region.Min.X),
		int32(region.Min.Y),
		uint32(region.Dx()),
		uint32(region.Dy()),
	)
	if err != nil {
		return Frame{}, err
	}

	if err := checkContext(ctx); err != nil {
		return Frame{}, err
	}

	return placeArea(img, meta, region), nil
}

// capturePipe performs one ScreenShot2 call: it makes a pipe, hands its write
// end to KWin beside the caller's arguments and the options dictionary, reads
// the reply's metadata and drains the pixels into an image.
//
// The pipe is how the image travels: ScreenShot2 duplicates the descriptor,
// writes the raw pixels into it from a worker thread and answers the method call
// before it starts writing. Reading after the reply is therefore safe, and a
// slow reader only makes KWin's writer wait.
func capturePipe(
	ctx context.Context,
	conn *dbus.Conn,
	service, method string,
	args ...any,
) (metadata, *image.RGBA, error) {
	// The image travels as a file descriptor, which needs a bus that negotiated
	// descriptor passing. A session bus that did not cannot take this path at
	// all, and reporting that as "not supported" lets the caller use the portal
	// instead of seeing an encoding fault from deep inside godbus.
	if !conn.SupportsUnixFDs() {
		return metadata{}, nil, errs.New(
			errs.CodeNotSupported,
			"this session's D-Bus does not pass file descriptors, and KWin writes a "+
				"screenshot into one",
		)
	}

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		return metadata{}, nil, errs.Wrap(
			err,
			errs.CodeInternal,
			"could not create the pipe KWin writes a screenshot into",
		)
	}
	defer readEnd.Close()

	callArgs := make([]any, 0, len(args)+2)
	callArgs = append(callArgs, args...)
	callArgs = append(callArgs, screenshotOptions(), dbus.UnixFD(int32(writeEnd.Fd())))

	var results map[string]dbus.Variant

	err = conn.Object(service, screenShotPath).
		CallWithContext(ctx, screenShotInterface+"."+method, noCallFlags, callArgs...).
		Store(&results)

	// KWin duplicates the descriptor before it answers, so this end is this
	// process's to close as soon as the call returns. Closing it here also means
	// the reader below sees EOF rather than waiting forever if KWin never writes.
	_ = writeEnd.Close()

	if err != nil {
		return metadata{}, nil, kwinError(err, "could not ask KWin for a screenshot")
	}

	values := make(map[string]any, len(results))
	for key, value := range results {
		values[key] = value.Value()
	}

	meta, err := metadataFrom(values)
	if err != nil {
		return metadata{}, nil, err
	}

	size, err := meta.byteLen()
	if err != nil {
		return metadata{}, nil, err
	}

	data, err := readPixels(ctx, readEnd, size)
	if err != nil {
		return metadata{}, nil, err
	}

	img, err := meta.rgba(data)
	if err != nil {
		return metadata{}, nil, err
	}

	return meta, img, nil
}

// readPixels drains exactly size bytes from the pipe KWin writes into.
//
// The read runs on its own goroutine so ctx can abandon it: os.File reads on a
// pipe are not context-aware, and closing the read end unblocks one that is
// waiting for pixels KWin never writes. The result channel is buffered, so an
// abandoned reader finishes and exits instead of blocking on a send.
func readPixels(ctx context.Context, pipe *os.File, size int) ([]byte, error) {
	data := make([]byte, size)

	type readResult struct {
		count int
		err   error
	}

	done := make(chan readResult, 1)

	go func() {
		count, err := io.ReadFull(pipe, data)
		done <- readResult{count: count, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, waitError(ctx)
	case read := <-done:
		if read.err != nil {
			return nil, errs.Wrapf(
				read.err,
				errs.CodeInternal,
				"KWin wrote %d of the %d bytes its metadata promised for the screenshot",
				read.count,
				size,
			)
		}

		return data, nil
	}
}
