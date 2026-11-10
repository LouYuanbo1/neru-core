//go:build linux

package wayland

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/LouYuanbo1/neru-core/errs"
)

// Conn is one connection to a Wayland compositor, used for screen capture. It
// speaks the core display protocol plus wl_shm, wl_output and
// wlr-screencopy; it never creates a surface.
//
// Its methods are serialized by an internal mutex, so one Conn may be shared
// between goroutines, but a capture in flight holds the others off. A Conn
// whose context deadline passed or whose connection broke is poisoned: every
// later call fails instead of resynchronizing an event stream whose replies
// have already been dropped.
type Conn struct {
	conn   *net.UnixConn
	reader *messageReader

	// mu serializes whole request/event exchanges. The reader goroutine never
	// takes it.
	mu sync.Mutex

	events     chan event
	readerDone chan struct{}
	closing    chan struct{}
	closeOnce  sync.Once

	errMu   sync.Mutex
	readErr error

	nextID  uint32
	objects map[uint32]struct{}

	registry          uint32
	shm               uint32
	shmFormats        map[uint32]bool
	screencopy        uint32
	screencopyVersion uint32
	outputs           []*outputState
	discovered        bool
	discoverErr       error
}

// event is one decoded protocol message: which object it names, which opcode,
// and a copy of its argument bytes.
type event struct {
	object uint32
	opcode uint16
	body   []byte
}

// Dial connects to the Wayland display named by $WAYLAND_DISPLAY under
// $XDG_RUNTIME_DIR. Wayland has no handshake, so a successful connect means the
// compositor accepted the client; protocol capabilities are discovered by
// Outputs and the first capture.
func Dial() (*Conn, error) {
	path, err := displaySocket(os.Getenv("WAYLAND_DISPLAY"))
	if err != nil {
		return nil, err
	}

	socket, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, errs.Wrapf(
			err,
			errs.CodeNotSupported,
			"screen capture is unavailable: could not connect to the Wayland compositor socket %q",
			path,
		)
	}

	conn := &Conn{
		conn:       socket,
		events:     make(chan event, 64),
		readerDone: make(chan struct{}),
		closing:    make(chan struct{}),
		nextID:     2,
		objects:    make(map[uint32]struct{}),
	}
	conn.reader = &messageReader{conn: socket}

	go conn.readLoop()

	return conn, nil
}

// Close closes the connection and waits for the reader goroutine to stop. It is
// safe to call at any time, including while another method is blocked; that
// method then fails with a connection error. Close is idempotent.
func (c *Conn) Close() error {
	c.closeSocket()
	<-c.readerDone

	return nil
}

// closeSocket tears the socket down once, which unblocks both the reader and a
// blocked write.
func (c *Conn) closeSocket() {
	c.closeOnce.Do(func() {
		close(c.closing)
		_ = c.conn.Close()
	})
}

// abort marks the connection unusable after a timeout, so a later call cannot
// read replies that belong to the abandoned request.
func (c *Conn) abort() {
	c.closeSocket()
}

// readLoop is the only reader of the socket. It decodes messages and hands them
// to method goroutines; when the connection dies it closes the event channel so
// every waiter observes the failure.
func (c *Conn) readLoop() {
	defer close(c.readerDone)
	defer close(c.events)

	for {
		ev, err := c.reader.readEvent()
		if err != nil {
			c.setReadErr(err)

			return
		}

		select {
		case c.events <- ev:
		case <-c.closing:
			return
		}
	}
}

// setReadErr records the first transport error.
func (c *Conn) setReadErr(err error) {
	c.errMu.Lock()
	defer c.errMu.Unlock()

	if c.readErr == nil {
		c.readErr = err
	}
}

// connectionError describes why the event channel closed.
func (c *Conn) connectionError() error {
	c.errMu.Lock()
	err := c.readErr
	c.errMu.Unlock()

	switch {
	case err == nil:
		return errs.New(errs.CodeInternal, "the Wayland connection is closed")
	case errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed):
		return errs.New(errs.CodeInternal, "the Wayland compositor closed the connection")
	default:
		return errs.Wrap(err, errs.CodeInternal, "the Wayland connection failed")
	}
}

// nextEvent waits for the next message. Cancellation closes the socket, which
// is the only way to unblock a read already in flight.
func (c *Conn) nextEvent(ctx context.Context) (event, error) {
	if err := checkContext(ctx); err != nil {
		return event{}, err
	}

	select {
	case ev, ok := <-c.events:
		if !ok {
			return event{}, c.connectionError()
		}

		return ev, nil
	case <-ctx.Done():
		c.abort()

		return event{}, contextError(ctx.Err())
	}
}

// send writes one request without file descriptors.
func (c *Conn) send(ctx context.Context, object uint32, opcode uint16, body []byte) error {
	message := encodeRequest(object, opcode, body)
	if len(message) > maxMessageSize {
		return errs.Newf(
			errs.CodeInternal,
			"a %d-byte Wayland request does not fit in one message",
			len(message),
		)
	}

	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}

	if err := writeAll(c.conn, message); err != nil {
		c.abort()

		return writeError(err)
	}

	return nil
}

// sendWithFD writes one request whose signature has an 'h' argument. The
// descriptor is not in the body: it is attached as SCM_RIGHTS ancillary data,
// which the kernel dups to the compositor. The caller keeps ownership of fd and
// must close it once the compositor no longer needs it (after the pool and
// buffer exist).
func (c *Conn) sendWithFD(
	ctx context.Context,
	object uint32,
	opcode uint16,
	body []byte,
	fd int,
) error {
	message := encodeRequest(object, opcode, body)
	if len(message) > maxMessageSize {
		return errs.Newf(
			errs.CodeInternal,
			"a %d-byte Wayland request does not fit in one message",
			len(message),
		)
	}

	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}

	written, _, err := c.conn.WriteMsgUnix(message, syscall.UnixRights(fd), nil)
	if err != nil {
		c.abort()

		return writeError(err)
	}

	// The descriptor went out with the first byte. A short write is possible in
	// principle, so finish the rest as plain data.
	for written < len(message) {
		n, err := c.conn.Write(message[written:])
		if err != nil {
			c.abort()

			return writeError(err)
		}

		written += n
	}

	return nil
}

// setWriteDeadline bounds a write by the caller's deadline. Without one the
// deadline is cleared, so a later call is not cut off by a stale one.
func (c *Conn) setWriteDeadline(ctx context.Context) error {
	var deadline time.Time
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}

	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return errs.Wrap(err, errs.CodeInternal, "could not set a write deadline on the Wayland socket")
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

// writeError maps a failed write onto the shared error vocabulary.
func writeError(err error) error {
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return errs.Wrap(err, errs.CodeTimeout, "the Wayland compositor did not accept a request in time")
	}

	return errs.Wrap(err, errs.CodeInternal, "could not send a request to the Wayland compositor")
}

// roundtrip sends wl_display.sync and drains events until its callback fires.
// handle, when non-nil, sees every event that is not the callback or a
// display-level message, which is how discovery collects globals and output
// state from the same stream.
func (c *Conn) roundtrip(ctx context.Context, handle func(event) error) error {
	callback := c.newID()
	c.register(callback)

	defer c.forget(callback)

	var body encoder
	body.uint32(callback)

	if err := c.send(ctx, displayID, displayOpSync, body.buf); err != nil {
		return err
	}

	for {
		ev, err := c.nextEvent(ctx)
		if err != nil {
			return err
		}

		if ev.object == callback {
			if ev.opcode != callbackEventDone {
				return errs.Newf(
					errs.CodeInternal,
					"the Wayland compositor sent opcode %d to a wl_callback",
					ev.opcode,
				)
			}

			return nil
		}

		handled, err := c.handleDisplayEvent(ev)
		if err != nil {
			return err
		}

		if handled || handle == nil {
			continue
		}

		if err := handle(ev); err != nil {
			return err
		}
	}
}

// handleDisplayEvent consumes messages addressed to wl_display itself: a
// protocol error is fatal to the operation, and delete_id only says an id may
// be reused (this package never reuses ids, so it is forgotten).
func (c *Conn) handleDisplayEvent(ev event) (bool, error) {
	if ev.object != displayID {
		return false, nil
	}

	switch ev.opcode {
	case displayEventError:
		d := decoder{buf: ev.body}

		object, err := d.uint32()
		if err != nil {
			return true, err
		}

		code, err := d.uint32()
		if err != nil {
			return true, err
		}

		message, err := d.string()
		if err != nil {
			return true, err
		}

		return true, errs.Newf(
			errs.CodeInternal,
			"the Wayland compositor reported a protocol error on object %d (code %d): %s",
			object,
			code,
			message,
		)
	case displayEventDeleteID:
		id, err := decodeUint32s(ev.body, 1)
		if err == nil {
			c.forget(id[0])
		}

		return true, nil
	default:
		return true, nil
	}
}

// newID allocates a client-side object id. Callers hold c.mu.
func (c *Conn) newID() uint32 {
	id := c.nextID
	c.nextID++

	return id
}

// register records that id is live, so a stray event can be told apart from a
// valid one if that is ever needed.
func (c *Conn) register(id uint32) {
	c.objects[id] = struct{}{}
}

// forget drops an object id the server no longer knows about.
func (c *Conn) forget(id uint32) {
	delete(c.objects, id)
}

// checkContext reports an already-finished context as the shared vocabulary
// expects, before any request is written.
func checkContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return contextError(err)
	}

	return nil
}

// contextError maps a context result onto CodeTimeout or CodeContextCanceled.
func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errs.Wrap(err, errs.CodeTimeout, "the Wayland compositor did not answer in time")
	}

	return errs.Wrap(err, errs.CodeContextCanceled, "screen capture canceled")
}

// messageReader frames messages out of the unix socket byte stream.
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
				return event{}, err
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
// descriptor is closed rather than leaked; the pool descriptor travels the
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
			return errs.New(errs.CodeInternal, "the Wayland compositor sent an oversized message")
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
