//go:build linux

package capture

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/LouYuanbo1/neru-core/errs"
	"github.com/LouYuanbo1/neru-core/vision/pixels"
)

// The Linux backend captures an X11 root window straight over the X wire
// protocol, so the library stays pure Go with no CGo, no libX11 and no
// third-party dependency. It connects to the socket $DISPLAY names, completes
// the X setup handshake (authenticating with MIT-MAGIC-COOKIE-1 when
// $XAUTHORITY has one), reads the root window's geometry and XGetImages the
// requested region off it.
//
// Only the X11 display server is supported; a Wayland session wears its own
// protocol this handshake does not speak, so it is refused with
// CodeNotSupported. A scaled X output returns pixels at its physical
// resolution, so a shot's scale can be greater than one (as on retina-mapped
// XWayland or a zoomed root).

const xOpcodeGetGeometry = 14
const xOpcodeGetImage = 73
const xAllPlanes = 0xFFFFFFFF

// xconn is one established X11 connection. Setup values the later requests
// need are kept here after the handshake.
type xconn struct {
	conn      net.Conn
	root      uint32
	rootDepth uint8
	lsbFirst  bool
}

// xdisplay parses $DISPLAY ("host:display.screen" or ":display.screen").
func xdisplay() (host string, display int, err error) {
	disp := os.Getenv("DISPLAY")
	if disp == "" {
		disp = ":0"
	}

	rest := disp
	if i := strings.LastIndexByte(rest, ':'); i >= 0 {
		host = rest[:i]
		rest = rest[i+1:]
	}
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		rest = rest[:dot]
	}

	display, err = strconv.Atoi(rest)
	if err != nil {
		return "", 0, fmt.Errorf("malformed DISPLAY %q", disp)
	}

	return host, display, nil
}

// dialX opens the connection the display names. A host that is empty or
// "localhost" is a local Unix-socket connection; a named host falls back to
// TCP so remote displays work too.
func dialX(host string, display int) (net.Conn, error) {
	if host == "" || host == "localhost" {
		sock := filepath.Join("/tmp/.X11-unix", "X"+strconv.Itoa(display))
		if conn, err := net.Dial("unix", sock); err == nil {
			return conn, nil
		}
	}

	return net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(6000+display)))
}

// xauthCookie finds the first MIT-MAGIC-COOKIE-1 data in $XAUTHORITY (default
// ~/.Xauthority). A nil result means "no usable cookie", which a server
// without access control still accepts.
func xauthCookie() []byte {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		path = filepath.Join(os.Getenv("HOME"), ".Xauthority")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	const magic = "MIT-MAGIC-COOKIE-1"
	i := 0
	for i < len(raw) {
		// Each entry is family(1) followed by four big-endian length-prefixed
		// fields: address, number, name, data.
		if i+3 > len(raw) {
			return nil
		}
		i++ // family
		var name, data []byte
		for field := 0; field < 4; field++ {
			if i+2 > len(raw) {
				return nil
			}
			n := int(binary.BigEndian.Uint16(raw[i : i+2]))
			i += 2
			if i+n > len(raw) {
				return nil
			}
			value := raw[i : i+n]
			i += n
			switch field {
			case 2:
				name = value
			case 3:
				data = value
			}
		}
		if string(name) == magic {
			return data
		}
	}

	return nil
}

// connect performs the setup handshake and caches the root window values.
func connect(ctx context.Context) (*xconn, error) {
	host, display, err := xdisplay()
	if err != nil {
		return nil, errs.Newf(errs.CodeInvalidInput, "%v", err)
	}

	conn, err := dialX(host, display)
	if err != nil {
		return nil, errs.Wrap(
			err,
			errs.CodeNotSupported,
			"screen capture is unavailable: could not connect to the X server",
		)
	}

	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}

	// Setup request: byte-order marker, protocol major/minor, then auth name
	// and data lengths, then the (padded) name and cookie.
	cookie := xauthCookie()
	req := make([]byte, 10)
	req[0] = 'l' // little-endian
	req[1] = 11  // protocol major
	req[2] = 0   // protocol minor
	req[3] = 0
	binary.LittleEndian.PutUint16(req[4:6], 0) // auth-name length
	binary.LittleEndian.PutUint16(req[6:8], uint16(len(cookie)))
	// req[8:10] unused
	req = append(req, cookie...)
	for len(req)%4 != 0 {
		req = append(req, 0)
	}

	if err := writeAll(ctx, conn, req); err != nil {
		conn.Close()
		return nil, err
	}

	var head [8]byte
	if _, err := readFull(ctx, conn, head[:]); err != nil {
		conn.Close()
		return nil, err
	}

	if head[0] != 1 {
		// Failure reply: success(1) unused(1) major(2) minor(2) reason-len(2),
		// then the reason string.
		reasonLen := int(binary.BigEndian.Uint16(head[6:8]))
		reason := make([]byte, reasonLen)
		if _, err := readFull(ctx, conn, reason); err != nil {
			conn.Close()
			return nil, err
		}
		conn.Close()
		return nil, errs.Newf(
			errs.CodeNotSupported,
			"screen capture is unavailable: the X server refused the connection: %s",
			string(reason),
		)
	}

	// Success reply additional data, laid out in 4-byte units:
	//   release(4) rid-base(2) rid-mask(2) motion(4) vendor-len(2)
	//   max-req(2) screens(1) formats(1) img-order(1) bitmap-order(1) ...
	extraLen := int(binary.BigEndian.Uint16(head[6:8])) * 4
	extra := make([]byte, extraLen)
	if _, err := readFull(ctx, conn, extra); err != nil {
		conn.Close()
		return nil, err
	}

	numFormats := int(extra[17])
	vendorLen := int(binary.BigEndian.Uint16(extra[12:14]))
	lsbFirst := extra[18] == 0 // image byte order: 0 = LSBFirst, 1 = MSBFirst

	// Fixed header is 28 bytes, then the padded vendor string, then the image
	// formats, then the first screen (root window id first).
	firstScreen := 28 + pad4(vendorLen) + 8*numFormats
	if firstScreen+40 > len(extra) {
		conn.Close()
		return nil, errs.New(errs.CodeInternal, "the X setup reply is malformed")
	}

	root := binary.LittleEndian.Uint32(extra[firstScreen : firstScreen+4])
	// Screen fixed header runs root(4) colormap(4) white(4) black(4) input(4)
	// width(2) height(2) mm(2) mm(2) maps(2) maps(2) visual(4) then
	// backing(1) save(1) root-depth(1) depths(1).
	rootDepth := extra[firstScreen+38]

	return &xconn{
		conn:      conn,
		root:      root,
		rootDepth: rootDepth,
		lsbFirst:  lsbFirst,
	}, nil
}

func pad4(n int) int { return (n + 3) &^ 3 }

// rootGeometry returns the root window's width and height via GetGeometry.
func (xc *xconn) rootGeometry(ctx context.Context) (int, int, error) {
	req := make([]byte, 12)
	req[0] = xOpcodeGetGeometry
	binary.LittleEndian.PutUint16(req[2:4], 3) // request length, 4-byte units
	binary.LittleEndian.PutUint32(req[4:8], xc.root)

	if err := writeAll(ctx, xc.conn, req); err != nil {
		return 0, 0, err
	}

	var rep [32]byte
	if _, err := readFull(ctx, xc.conn, rep[:]); err != nil {
		return 0, 0, err
	}
	if rep[0] != 1 {
		return 0, 0, errs.New(errs.CodeInternal, "unexpected X message during geometry")
	}

	return int(binary.LittleEndian.Uint16(rep[16:18])),
		int(binary.LittleEndian.Uint16(rep[18:20])), nil
}

// readImage XGetImages the given physical-pixel region and decodes the
// returned ZPixmap into RGBA. Pixels are 4 bytes each (32bpp) with the setup's
// image byte order deciding where red sits.
func (xc *xconn) readImage(ctx context.Context, region image.Rectangle) (*image.RGBA, error) {
	w, h := region.Dx(), region.Dy()
	if w <= 0 || h <= 0 {
		return nil, errs.Newf(errs.CodeInvalidInput, "capture region %v is empty", region)
	}

	req := make([]byte, 20)
	req[0] = xOpcodeGetImage
	req[1] = xc.rootDepth
	binary.LittleEndian.PutUint16(req[2:4], 5) // request length
	binary.LittleEndian.PutUint32(req[4:8], xc.root)
	binary.LittleEndian.PutUint16(req[8:10], uint16(int16(region.Min.X)))
	binary.LittleEndian.PutUint16(req[10:12], uint16(int16(region.Min.Y)))
	binary.LittleEndian.PutUint16(req[12:14], uint16(w))
	binary.LittleEndian.PutUint16(req[14:16], uint16(h))
	binary.LittleEndian.PutUint32(req[16:20], xAllPlanes)

	if err := writeAll(ctx, xc.conn, req); err != nil {
		return nil, err
	}

	var head [8]byte
	if _, err := readFull(ctx, xc.conn, head[:]); err != nil {
		return nil, err
	}
	if head[0] != 1 {
		return nil, errs.New(errs.CodeInternal, "unexpected X message during image read")
	}
	if head[1] != xc.rootDepth {
		return nil, errs.New(
			errs.CodeNotSupported,
			"the X server returned a capture depth the library cannot read",
		)
	}

	// The `length` field counts every 4-byte unit after this 8-byte header,
	// which includes the 4-byte visual id and 20 bytes of padding before the
	// pixel data.
	total := int(binary.LittleEndian.Uint32(head[4:8])) * 4
	buf := make([]byte, total)
	if _, err := readFull(ctx, xc.conn, buf); err != nil {
		return nil, err
	}

	data := buf[24:] // skip visual(4) + pad(20)
	row := w * 4
	if row*h > len(data) {
		return nil, errs.New(errs.CodeInternal, "the X capture was shorter than the requested region")
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		src := data[y*row:]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			p := x * 4
			if xc.lsbFirst {
				// 32-bit pixel LSB-first: B G R pad.
				dst[p+0], dst[p+1], dst[p+2], dst[p+3] = src[p+2], src[p+1], src[p+0], 0xff
			} else {
				// 32-bit pixel MSB-first: pad R G B.
				dst[p+0], dst[p+1], dst[p+2], dst[p+3] = src[p+1], src[p+2], src[p+3], 0xff
			}
		}
	}

	return img, nil
}

// scaleAt reports frame pixels per logical pixel. X11 has no cheap
// per-output scale on the wire here, so unless a future build reads a scaling
// hint the shot reports scale 1, meaning logical == physical; retina-mapped
// XWayland and desktop zoom are captured physical-as-logical, which vision
// still reads consistently.
func (xc *xconn) scaleAt() float64 { return 1 }

func (xc *xconn) close() { xc.conn.Close() }

// captureX11Screen captures the whole root window. Origin is (0,0) and Region
// the full bitmap. The platform entry point in linux_dispatch.go calls it only
// on an X11 session.
func captureX11Screen(ctx context.Context) (pixels.Frame, error) {
	xc, err := connect(ctx)
	if err != nil {
		return pixels.Frame{}, err
	}
	defer xc.close()

	rw, rh, err := xc.rootGeometry(ctx)
	if err != nil {
		return pixels.Frame{}, err
	}

	img, err := xc.readImage(ctx, image.Rect(0, 0, rw, rh))
	if err != nil {
		return pixels.Frame{}, err
	}

	return pixels.Frame{
		Img:    img,
		Origin: image.Point{},
		Scale:  xc.scaleAt(),
		Region: img.Rect,
	}, nil
}

// captureX11Rect captures exactly the given logical region off the root window.
// Origin is the region's top-left, Region the region itself. The platform entry
// point in linux_dispatch.go calls it only on an X11 session.
func captureX11Rect(ctx context.Context, region image.Rectangle) (pixels.Frame, error) {
	if err := ctx.Err(); err != nil {
		return pixels.Frame{}, errs.Wrap(ctx.Err(), errs.CodeContextCanceled, "capture canceled")
	}

	region = region.Canon()
	if region.Empty() {
		return pixels.Frame{}, errs.Newf(
			errs.CodeInvalidInput,
			"region capture needs a region to capture; %v is empty",
			region,
		)
	}

	xc, err := connect(ctx)
	if err != nil {
		return pixels.Frame{}, err
	}
	defer xc.close()

	img, err := xc.readImage(ctx, region)
	if err != nil {
		return pixels.Frame{}, err
	}

	return pixels.Frame{
		Img:    img,
		Origin: region.Min,
		Scale:  xc.scaleAt(),
		Region: region,
	}, nil
}

// writeAll writes p fully, honoring a canceled context.
func writeAll(ctx context.Context, conn net.Conn, p []byte) error {
	for len(p) > 0 {
		select {
		case <-ctx.Done():
			return errs.Wrap(ctx.Err(), errs.CodeContextCanceled, "capture canceled")
		default:
		}
		n, err := conn.Write(p)
		if err != nil {
			return errs.Wrap(err, errs.CodeAccessibilityFailed, "could not write to the X server")
		}
		p = p[n:]
	}
	return nil
}

// readFull reads exactly len(p) bytes, honoring a canceled context by closing
// the connection, which unblocks the reader.
func readFull(ctx context.Context, conn net.Conn, p []byte) (int, error) {
	done := make(chan error, 1)
	go func() {
		done <- readFullBlocking(conn, p)
	}()

	select {
	case err := <-done:
		if err != nil {
			return 0, err
		}
		return len(p), nil
	case <-ctx.Done():
		conn.Close()
		return 0, errs.Wrap(ctx.Err(), errs.CodeContextCanceled, "capture canceled")
	}
}

// readFullBlocking reads exactly len(p) bytes.
func readFullBlocking(conn net.Conn, p []byte) error {
	for len(p) > 0 {
		n, err := conn.Read(p)
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				return errs.New(errs.CodeContextCanceled, "capture canceled")
			}
			return errs.Wrap(err, errs.CodeAccessibilityFailed, "could not read from the X server")
		}
		p = p[n:]
	}
	return nil
}
