//go:build linux

package portal

import (
	"context"

	"github.com/godbus/dbus/v5"

	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// grab runs one screenshot request and returns the frame it produced: the
// pixels decoded from the PNG the portal wrote, placed on the layout the
// compositor reports.
//
// The order matters. The layout probe starts first because it is the part that
// talks to the compositor and the portal exchange is the part that waits for a
// dialog; overlapping them means the capture pays for the layout only when the
// compositor answers more slowly than the portal does. The signal subscription
// is installed before the method call because the portal is allowed to answer
// the moment the call returns, and the request handle the call returns is
// accepted beside the one its token predicts.
func grab(ctx context.Context) (pixels.Frame, error) {
	ctx, cancel := withBudget(ctx)
	defer cancel()

	probed := startDesktop()

	conn, err := dialSessionBus(ctx)
	if err != nil {
		return pixels.Frame{}, err
	}
	defer conn.Close()

	sender, err := uniqueName(conn)
	if err != nil {
		return pixels.Frame{}, err
	}

	token := nextRequestToken()
	derived := requestHandle(sender, token)

	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)

	err = conn.AddMatchSignalContext(
		ctx,
		dbus.WithMatchInterface(requestInterface),
		dbus.WithMatchMember(responseMember),
		dbus.WithMatchPathNamespace(dbus.ObjectPath(requestNamespace(sender))),
	)
	if err != nil {
		return pixels.Frame{}, portalCallError(err, "could not subscribe to the screenshot portal's answer")
	}

	handle, err := callScreenshot(ctx, conn, token)
	if err != nil {
		return pixels.Frame{}, err
	}

	handles := []dbus.ObjectPath{derived}
	if handle != "" && handle != derived {
		handles = append(handles, handle)
	}

	results, err := awaitResponse(ctx, signals, requestNamespace(sender), handles)
	if err != nil {
		return pixels.Frame{}, err
	}

	path, err := resultFilePath(results)
	if err != nil {
		return pixels.Frame{}, err
	}

	img, err := decodeFile(path)
	if err != nil {
		return pixels.Frame{}, err
	}

	// Decoding a 4K PNG takes long enough that the caller may have given up
	// meanwhile, and returning pixels nobody waits for would report a capture
	// that the caller already counts as canceled.
	if err := checkContext(ctx); err != nil {
		return pixels.Frame{}, err
	}

	return place(img, <-probed), nil
}
