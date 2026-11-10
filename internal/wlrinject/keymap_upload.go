//go:build linux

package wlrinject

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/LouYuanbo1/neru-core/errs"
)

// The virtual keyboard's keymap travels as a file descriptor, so the bytes have
// to live in something the compositor can mmap. This client uses an unlinked
// file in a shared-memory directory rather than memfd_create: the syscall
// number is not exposed by the standard library on every architecture this
// package cross-compiles for (amd64 among them), and an unlinked mkstemp file is
// exactly what neru's C client and internal/wayland's shm pools already use.
// The descriptor is unlinked before it is sent, so nothing is left behind on
// any path, and the compositor's SCM_RIGHTS duplicate keeps the object alive
// after this process closes its own.

// keymapFilePattern names the unlinked keymap file in diagnostics.
const keymapFilePattern = "neru-wlrinject-keymap-*.xkb"

// uploadKeymap writes the embedded keymap to shared memory, sends it with the
// SCM_RIGHTS descriptor, and roundtrips so the compositor has compiled it
// before Dial returns.
//
// The size includes the trailing NUL: an XKB v1 keymap is a C string, and
// xkbcommon requires the terminator. The descriptor is closed as soon as the
// request is on the wire; the compositor holds its own reference from then on.
func (c *Client) uploadKeymap() error {
	// keymapXKB is immutable, so the NUL is appended to a copy.
	data := make([]byte, len(keymapXKB)+1)
	copy(data, keymapXKB)

	fd, cleanup, err := createKeymapFile(data)
	if err != nil {
		return errs.Wrap(
			err,
			errs.CodeAccessibilityFailed,
			"could not create the shared-memory file for the virtual keyboard keymap",
		)
	}
	defer cleanup()

	var body encoder

	body.uint32(keymapFormatXKBV1)
	body.uint32(uint32(len(data)))

	if err := c.sendWithFD(c.keyboard, virtualKeyboardOpKeymap, body.buf, fd); err != nil {
		return err
	}

	return c.roundtrip(nil)
}

// createKeymapFile creates an unlinked file holding data and returns its
// descriptor plus the cleanup that closes it. It tries the per-user runtime
// directory first, which is the tmpfs Wayland itself lives on, then /dev/shm,
// then the process temporary directory.
func createKeymapFile(data []byte) (int, func(), error) {
	var lastErr error

	for _, dir := range keymapDirs() {
		fd, cleanup, err := createKeymapFileIn(dir, data)
		if err == nil {
			return fd, cleanup, nil
		}

		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no writable directory for a keymap file")
	}

	return -1, func() {}, lastErr
}

// createKeymapFileIn creates, unlinks and fills one keymap file in dir.
func createKeymapFileIn(dir string, data []byte) (int, func(), error) {
	file, err := os.CreateTemp(dir, keymapFilePattern)
	if err != nil {
		return -1, nil, err
	}

	// Unlink immediately: the descriptor keeps the object alive, the compositor
	// inherits a duplicate through SCM_RIGHTS, and a failed upload leaves
	// nothing on disk.
	_ = os.Remove(file.Name())

	fd := int(file.Fd())

	if err := writeAllFD(fd, data); err != nil {
		_ = file.Close()

		return -1, nil, err
	}

	return fd, func() { _ = file.Close() }, nil
}

// writeAllFD writes every byte of data to fd, handling short writes and
// interrupted calls.
func writeAllFD(fd int, data []byte) error {
	for len(data) > 0 {
		written, err := syscall.Write(fd, data)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}

			return err
		}

		if written == 0 {
			return io.ErrShortWrite
		}

		data = data[written:]
	}

	return nil
}

// keymapDirs lists the candidate shared-memory directories, in preference
// order and without duplicates.
func keymapDirs() []string {
	dirs := make([]string, 0, 3)

	add := func(dir string) {
		if dir == "" {
			return
		}

		for _, existing := range dirs {
			if existing == dir {
				return
			}
		}

		dirs = append(dirs, dir)
	}

	add(os.Getenv("XDG_RUNTIME_DIR"))
	add("/dev/shm")
	add(os.TempDir())

	return dirs
}
