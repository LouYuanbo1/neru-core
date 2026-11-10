//go:build linux

package wayland

import (
	"context"
	"image"

	"github.com/LouYuanbo1/neru-core/errs"
)

// CaptureOutput captures one output at its native pixel size. The image is
// anchored at (0,0) and is as large as the output's current mode; origin is the
// output's logical top-left corner on the desktop layout, and scale is frame
// pixels per logical pixel, so a caller can place the image and convert
// coordinates into it.
//
// output must be a value this Conn's Outputs returned (its GlobalName is what
// identifies it); a zero GlobalName falls back to matching the layout geometry.
// Rotated outputs, linux-dmabuf-only compositors and pixel formats other than
// XRGB8888/ARGB8888 fail with CodeNotSupported.
func (c *Conn) CaptureOutput(
	ctx context.Context,
	output Output,
) (img *image.RGBA, origin image.Point, scale float64, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := checkContext(ctx); err != nil {
		return nil, image.Point{}, 0, err
	}

	if err := c.discover(ctx); err != nil {
		return nil, image.Point{}, 0, err
	}

	state := c.findOutput(output)
	if state == nil {
		return nil, image.Point{}, 0, errs.Newf(
			errs.CodeInvalidInput,
			"the compositor has no output matching %q (global %d)",
			output.Name,
			output.GlobalName,
		)
	}

	snapshot, ok := state.snapshot()
	if !ok {
		return nil, image.Point{}, 0, errs.Newf(
			errs.CodeNotSupported,
			"the compositor has not reported a mode for output %q",
			state.name(),
		)
	}

	captured, err := c.capture(ctx, state)
	if err != nil {
		return nil, image.Point{}, 0, err
	}

	return captured, image.Pt(snapshot.X, snapshot.Y), float64(snapshot.Scale), nil
}

// CaptureRegion captures a logical desktop region, clipped to the one output
// whose layout rectangle contains the region's centre. The returned image
// covers the clipped region at that output's scale, its origin is the clipped
// region's logical top-left, and scale is frame pixels per logical pixel.
//
// Picking the output by the region's centre rather than by a corner means a
// region that starts exactly on an output boundary stays with the output that
// holds most of it. A region outside every output fails with CodeInvalidInput;
// a compositor with no outputs fails with CodeNotSupported.
func (c *Conn) CaptureRegion(
	ctx context.Context,
	region image.Rectangle,
) (*image.RGBA, image.Point, float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := checkContext(ctx); err != nil {
		return nil, image.Point{}, 0, err
	}

	region = region.Canon()
	if region.Empty() {
		return nil, image.Point{}, 0, errs.Newf(
			errs.CodeInvalidInput,
			"capture region %v is empty",
			region,
		)
	}

	if err := c.discover(ctx); err != nil {
		return nil, image.Point{}, 0, err
	}

	outputs := c.snapshotOutputs()
	if len(outputs) == 0 {
		return nil, image.Point{}, 0, errs.New(
			errs.CodeNotSupported,
			"the compositor reports no outputs to capture",
		)
	}

	target, ok := outputAt(outputs, regionCenter(region))
	if !ok {
		return nil, image.Point{}, 0, errs.Newf(
			errs.CodeInvalidInput,
			"no output covers the centre of capture region %v",
			region,
		)
	}

	state := c.outputByGlobalName(target.GlobalName)
	if state == nil {
		return nil, image.Point{}, 0, errs.Newf(
			errs.CodeInternal,
			"output %q disappeared during the capture",
			target.Name,
		)
	}

	captured, err := c.capture(ctx, state)
	if err != nil {
		return nil, image.Point{}, 0, err
	}

	clipped := region.Intersect(outputRect(target))
	pixels, err := cropImage(
		captured,
		logicalToPixel(clipped, image.Pt(target.X, target.Y), float64(target.Scale)),
	)
	if err != nil {
		return nil, image.Point{}, 0, err
	}

	return pixels, clipped.Min, float64(target.Scale), nil
}

// capture runs one zwlr_screencopy_frame_v1 exchange: ask for a frame, let the
// compositor describe the wl_shm buffer it wants, allocate and map that buffer,
// submit it, wait for ready, then convert it.
func (c *Conn) capture(ctx context.Context, output *outputState) (*image.RGBA, error) {
	if err := checkOutputCapturable(output); err != nil {
		return nil, err
	}

	if len(c.shmFormats) > 0 &&
		!c.supportsShmFormat(shmFormatXRGB8888) &&
		!c.supportsShmFormat(shmFormatARGB8888) {
		return nil, errs.New(
			errs.CodeNotSupported,
			"the compositor's wl_shm advertises neither XRGB8888 nor ARGB8888, so the frame could not be read",
		)
	}

	frame := c.newID()
	c.register(frame)
	// The frame is destroyed on every path. A failed send has already poisoned
	// the connection, and the deferred send is then a no-op error.
	defer c.destroyObject(ctx, frame, frameOpDestroy)

	var body encoder
	body.uint32(frame)
	body.int32(0) // overlay_cursor: never composite the pointer
	body.uint32(output.wlName)

	if err := c.send(ctx, c.screencopy, screencopyOpCaptureOutput, body.buf); err != nil {
		return nil, err
	}

	info, err := c.awaitBuffer(ctx, frame)
	if err != nil {
		return nil, err
	}

	if !info.hasBuffer {
		return nil, errs.Newf(
			errs.CodeNotSupported,
			"the compositor offered no wl_shm buffer for output %q, only linux-dmabuf; this pure-Go capture reads wl_shm buffers",
			output.name(),
		)
	}

	if err := info.validate(); err != nil {
		return nil, err
	}

	size := info.stride * info.height

	buffer, err := createShm(size)
	if err != nil {
		return nil, err
	}
	defer buffer.close()

	pool := c.newID()
	c.register(pool)
	defer c.destroyObject(ctx, pool, shmPoolOpDestroy)

	body = encoder{}
	body.uint32(pool)
	body.int32(int32(size))

	if err := c.sendWithFD(ctx, c.shm, shmOpCreatePool, body.buf, int(buffer.file.Fd())); err != nil {
		return nil, err
	}

	wlBuffer := c.newID()
	c.register(wlBuffer)
	defer c.destroyObject(ctx, wlBuffer, bufferOpDestroy)

	body = encoder{}
	body.uint32(wlBuffer)
	body.int32(0) // offset into the pool
	body.int32(int32(info.width))
	body.int32(int32(info.height))
	body.int32(int32(info.stride))
	body.uint32(info.format)

	if err := c.send(ctx, pool, shmPoolOpCreateBuffer, body.buf); err != nil {
		return nil, err
	}

	// The pool object is empty once the buffer exists, and the descriptor has
	// been duplicated by the compositor. The mapping below keeps the memory
	// readable in this process.
	c.destroyObject(ctx, pool, shmPoolOpDestroy)
	buffer.closeFile()

	body = encoder{}
	body.uint32(wlBuffer)

	if err := c.send(ctx, frame, frameOpCopy, body.buf); err != nil {
		return nil, err
	}

	if err := c.awaitReady(ctx, frame, &info); err != nil {
		return nil, err
	}

	return convertShmBuffer(
		buffer.data,
		info.width,
		info.height,
		info.stride,
		info.format,
		info.yInvert,
	)
}

// destroyObject sends a destructor request and forgets the id. It is
// idempotent, so an explicit destroy can be followed by a deferred one without
// sending a second request to an object the compositor has already released.
func (c *Conn) destroyObject(ctx context.Context, id uint32, opcode uint16) {
	if _, live := c.objects[id]; !live {
		return
	}

	_ = c.send(ctx, id, opcode, nil)
	c.forget(id)
}

// awaitBuffer drains the frame's buffer negotiation: the buffer event names the
// wl_shm buffer to allocate, linux_dmabuf reports a path this package cannot
// use, and buffer_done says the enumeration is complete. Manager version 1 and
// 2 send no buffer_done, so the buffer event is the end of negotiation there.
func (c *Conn) awaitBuffer(ctx context.Context, frame uint32) (shmBufferInfo, error) {
	var info shmBufferInfo

	for {
		ev, err := c.nextEvent(ctx)
		if err != nil {
			return info, err
		}

		if handled, err := c.handleDisplayEvent(ev); handled {
			if err != nil {
				return info, err
			}

			continue
		}

		if ev.object != frame {
			continue
		}

		switch ev.opcode {
		case frameEventBuffer:
			values, err := decodeUint32s(ev.body, 4)
			if err != nil {
				return info, err
			}

			if !info.hasBuffer {
				info = shmBufferInfo{
					format:    values[0],
					width:     int(values[1]),
					height:    int(values[2]),
					stride:    int(values[3]),
					hasBuffer: true,
				}
			}

			if c.screencopyVersion < 3 {
				return info, nil
			}
		case frameEventLinuxDMABuf:
			// Not consumed: only XRGB/ARGB wl_shm frames are read. The event is
			// still parsed so a dmabuf-only compositor is reported accurately.
			if _, err := decodeUint32s(ev.body, 3); err != nil {
				return info, err
			}
		case frameEventBufferDone:
			return info, nil
		case frameEventFailed:
			return info, errs.New(
				errs.CodeInternal,
				"the compositor failed to capture the output",
			)
		case frameEventReady:
			return info, errs.New(
				errs.CodeInternal,
				"the compositor sent a ready frame before describing a buffer",
			)
		}
	}
}

// awaitReady waits for flags and ready, and fails on failed. Damage is only
// sent in response to copy_with_damage, which this package never uses.
func (c *Conn) awaitReady(ctx context.Context, frame uint32, info *shmBufferInfo) error {
	for {
		ev, err := c.nextEvent(ctx)
		if err != nil {
			return err
		}

		if handled, err := c.handleDisplayEvent(ev); handled {
			if err != nil {
				return err
			}

			continue
		}

		if ev.object != frame {
			continue
		}

		switch ev.opcode {
		case frameEventFlags:
			values, err := decodeUint32s(ev.body, 1)
			if err != nil {
				return err
			}

			info.yInvert = values[0]&frameFlagYInvert != 0
		case frameEventReady:
			// The presentation timestamp is not needed for a still frame.
			if _, err := decodeUint32s(ev.body, 3); err != nil {
				return err
			}

			return nil
		case frameEventFailed:
			return errs.New(
				errs.CodeInternal,
				"the compositor failed to copy the output into the shared-memory buffer",
			)
		case frameEventDamage:
			if _, err := decodeUint32s(ev.body, 4); err != nil {
				return err
			}
		}
	}
}
