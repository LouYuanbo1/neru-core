//go:build linux

package wayland

import (
	"os"
	"syscall"

	"github.com/LouYuanbo1/neru-core/errs"
)

// shmBuffer is one shared-memory pool: a file whose descriptor is handed to the
// compositor, and the same bytes mapped into this process so the copied frame
// can be read without another roundtrip.
type shmBuffer struct {
	file *os.File
	data []byte
}

// createShm allocates size bytes of shared memory. It prefers $XDG_RUNTIME_DIR,
// which is the per-user tmpfs Wayland itself lives on, then /dev/shm, then the
// process temporary directory.
func createShm(size int) (*shmBuffer, error) {
	var lastErr error

	for _, dir := range shmDirs() {
		buffer, err := createShmIn(dir, size)
		if err == nil {
			return buffer, nil
		}

		lastErr = err
	}

	if lastErr == nil {
		lastErr = errs.New(errs.CodeInternal, "no writable directory for a shared-memory buffer")
	}

	return nil, errs.Wrap(
		lastErr,
		errs.CodeInternal,
		"could not allocate a shared-memory buffer for the screen capture",
	)
}

// shmDirs lists the candidate directories, in preference order and without
// duplicates.
func shmDirs() []string {
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

// createShmIn creates and maps one pool file in dir.
func createShmIn(dir string, size int) (*shmBuffer, error) {
	file, err := os.CreateTemp(dir, "neru-screencopy-*.shm")
	if err != nil {
		return nil, err
	}

	// Unlink immediately: the descriptor and the mapping keep the object alive,
	// the compositor inherits the descriptor through SCM_RIGHTS, and nothing is
	// left behind if the capture fails or the process dies.
	_ = os.Remove(file.Name())

	if err := file.Truncate(int64(size)); err != nil {
		_ = file.Close()

		return nil, err
	}

	data, err := syscall.Mmap(
		int(file.Fd()),
		0,
		size,
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED,
	)
	if err != nil {
		_ = file.Close()

		return nil, err
	}

	return &shmBuffer{file: file, data: data}, nil
}

// closeFile releases the pool file descriptor once the compositor has its own
// copy. The mapping stays valid.
func (b *shmBuffer) closeFile() {
	if b.file != nil {
		_ = b.file.Close()
		b.file = nil
	}
}

// close unmaps and closes the pool. It is idempotent.
func (b *shmBuffer) close() {
	if b.data != nil {
		// The mapping held screen pixels. Wipe it before unmapping so the pages
		// go back to the kernel blank, as the X11 capture path does.
		clear(b.data)
		_ = syscall.Munmap(b.data)
		b.data = nil
	}

	b.closeFile()
}
