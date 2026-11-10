//go:build linux

package wlrinject

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// opTimeout bounds one complete request/response exchange: one action's writes
// plus the wl_display.sync roundtrip that confirms the compositor processed
// them. A local compositor answers in well under a millisecond; the bound is
// what turns a wedged socket into errs.CodeTimeout instead of a hung caller.
const opTimeout = 2 * time.Second

// errMalformed marks a byte stream that does not decode as Wayland messages.
// It is a protocol failure of the connection, not of one request, so it poisons
// the client like a transport error does.
var errMalformed = errors.New("wlrinject: malformed Wayland message")

// event is one decoded protocol message: which object it names, which opcode,
// and a copy of its argument bytes.
type event struct {
	object uint32
	opcode uint16
	body   []byte
}

// send writes one request without file descriptors.
func (c *Client) send(object uint32, opcode uint16, body []byte) error {
	message := encodeRequest(object, opcode, body)
	if len(message) > maxMessageSize {
		return errs.Newf(
			errs.CodeInternal,
			"a %d-byte Wayland request does not fit in one message",
			len(message),
		)
	}

	if err := writeAll(c.conn, message); err != nil {
		return c.transportError(err)
	}

	return nil
}

// sendWithFD writes one request whose signature has an 'h' argument. The
// descriptor is not in the body: it is attached as SCM_RIGHTS ancillary data on
// the message's first byte, which the kernel dups to the compositor. The caller
// keeps ownership of fd and closes it once the request has been written; the
// compositor holds its own reference from then on.
func (c *Client) sendWithFD(object uint32, opcode uint16, body []byte, fd int) error {
	message := encodeRequest(object, opcode, body)
	if len(message) > maxMessageSize {
		return errs.Newf(
			errs.CodeInternal,
			"a %d-byte Wayland request does not fit in one message",
			len(message),
		)
	}

	written, _, err := c.conn.WriteMsgUnix(message, syscall.UnixRights(fd), nil)
	if err != nil {
		return c.transportError(err)
	}

	// The descriptor went out with the first byte. A short write is possible in
	// principle, so finish the rest as plain data.
	for written < len(message) {
		n, err := c.conn.Write(message[written:])
		if err != nil {
			return c.transportError(err)
		}

		written += n
	}

	return nil
}

// writeAll writes all of p, handling short writes.
func writeAll(conn net.Conn, p []byte) error {
	for len(p) > 0 {
		n, err := conn.Write(p)
		if err != nil {
			return err
		}

		p = p[n:]
	}

	return nil
}

// roundtrip sends wl_display.sync and reads events until its callback fires.
// handle, when non-nil, sees every event that is neither the callback nor a
// display-level message, which is how Dial collects globals, seat capabilities
// and output geometry from the same stream. Events nobody handles are ignored:
// this client subscribes to no other object and no other event carries state it
// needs.
func (c *Client) roundtrip(handle func(event) error) error {
	callback := c.newID()

	var body encoder

	body.uint32(callback)

	if err := c.send(displayID, displayOpSync, body.buf); err != nil {
		return err
	}

	for {
		ev, err := c.reader.readEvent()
		if err != nil {
			return c.transportError(err)
		}

		if ev.object == displayID {
			if err := c.handleDisplayEvent(ev); err != nil {
				return c.poisonWith(err)
			}

			continue
		}

		if ev.object == callback {
			if ev.opcode != callbackEventDone {
				return c.poisonWith(errs.Newf(
					errs.CodeAccessibilityFailed,
					"the Wayland compositor sent opcode %d to a wl_callback",
					ev.opcode,
				))
			}

			return nil
		}

		if handle == nil {
			continue
		}

		if err := handle(ev); err != nil {
			return c.poisonWith(errs.Wrap(
				err,
				errs.CodeAccessibilityFailed,
				"the Wayland compositor sent a message this client could not decode",
			))
		}
	}
}

// handleDisplayEvent consumes the messages addressed to wl_display itself. A
// protocol error is fatal to the whole connection. delete_id only says an id
// may be reused; this client never reuses ids, so it is ignored.
func (c *Client) handleDisplayEvent(ev event) error {
	switch ev.opcode {
	case displayEventError:
		d := decoder{buf: ev.body}

		object, err := d.uint32()
		if err != nil {
			return errs.Wrap(err, errs.CodeAccessibilityFailed,
				"the Wayland compositor sent a protocol error this client could not decode")
		}

		code, err := d.uint32()
		if err != nil {
			return errs.Wrap(err, errs.CodeAccessibilityFailed,
				"the Wayland compositor sent a protocol error this client could not decode")
		}

		message, err := d.string()
		if err != nil {
			return errs.Wrap(err, errs.CodeAccessibilityFailed,
				"the Wayland compositor sent a protocol error this client could not decode")
		}

		return errs.Newf(
			errs.CodeAccessibilityFailed,
			"the Wayland compositor reported a protocol error on object %d (code %d): %s",
			object,
			code,
			message,
		)
	case displayEventDeleteID:
		// Ids are allocated monotonically and never reused, so there is nothing
		// to forget. The event still has to be consumed to keep the stream in
		// sync, which reading it already did.
		return nil
	default:
		return nil
	}
}

// newID allocates a client-side object id. The caller holds c.mu.
func (c *Client) newID() uint32 {
	id := c.nextID
	c.nextID++

	return id
}

// transportError records why the connection cannot be used any more, closes it,
// and returns the shared-vocabulary error for the caller. The first error wins;
// calling it twice cannot replace a timeout with the EOF that follows.
func (c *Client) transportError(err error) error {
	if errors.Is(err, errMalformed) {
		return c.poisonWith(errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"the Wayland compositor sent a malformed message",
		))
	}

	if isTimeout(err) {
		return c.poisonWith(errs.Wrapf(
			err,
			errs.CodeTimeout,
			"the Wayland compositor did not answer within %s",
			opTimeout,
		))
	}

	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return c.poisonWith(errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"the Wayland compositor closed the connection",
		))
	}

	return c.poisonWith(errs.Wrap(
		err,
		errs.CodeAccessibilityFailed,
		"the Wayland connection failed",
	))
}

// poisonWith stores the first fatal error and tears the socket down, so a later
// call cannot read replies that belong to an abandoned request.
func (c *Client) poisonWith(err error) error {
	if c.poison == nil {
		c.poison = err
	}

	c.closeSocket()

	return c.poison
}

// closeSocket closes the connection once. Every later call is a no-op.
func (c *Client) closeSocket() {
	c.closeOnce.Do(func() {
		_ = c.conn.Close()
	})
}

// isTimeout reports whether err is a socket deadline.
func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}

	var netErr net.Error

	return errors.As(err, &netErr) && netErr.Timeout()
}

// messageReader frames messages out of the unix socket byte stream. It is only
// ever used by the goroutine that holds the client's lock, because every
// exchange reads to its own roundtrip callback.
type messageReader struct {
	conn *net.UnixConn
	buf  []byte
	off  int

	tmp [8192]byte
	oob [256]byte
}

// readEvent returns the next complete message, reading more bytes as needed.
// The returned body is a copy, because the reader buffer is reused.
func (r *messageReader) readEvent() (event, error) {
	for {
		available := r.buf[r.off:]
		if len(available) >= headerSize {
			header, err := decodeHeader(available)
			if err != nil {
				return event{}, fmt.Errorf("%w: %v", errMalformed, err)
			}

			if len(available) >= header.size {
				body := make([]byte, header.size-headerSize)
				copy(body, available[headerSize:header.size])
				r.off += header.size

				return event{object: header.object, opcode: header.opcode, body: body}, nil
			}
		}

		if err := r.fill(); err != nil {
			return event{}, err
		}
	}
}

// fill reads more bytes from the socket and discards any file descriptors that
// arrive. No event this package subscribes to carries one, so an unexpected
// descriptor is closed rather than leaked; the keymap descriptor travels the
// other way, in sendWithFD.
func (r *messageReader) fill() error {
	if r.off > 0 {
		remaining := copy(r.buf, r.buf[r.off:])
		r.buf = r.buf[:remaining]
		r.off = 0
	}

	n, oobn, _, _, err := r.conn.ReadMsgUnix(r.tmp[:], r.oob[:])
	if n > 0 {
		r.buf = append(r.buf, r.tmp[:n]...)
		if oobn > 0 {
			closeReceivedFDs(r.oob[:oobn])
		}

		if len(r.buf) > maxMessageSize*2 {
			return fmt.Errorf("%w: oversized message", errMalformed)
		}

		return nil
	}

	if err != nil {
		return err
	}

	return io.EOF
}

// closeReceivedFDs closes every descriptor in a SCM_RIGHTS control message.
func closeReceivedFDs(oob []byte) {
	messages, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}

	for i := range messages {
		fds, err := syscall.ParseUnixRights(&messages[i])
		if err != nil {
			continue
		}

		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
	}
}
