package listener

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// staleCheckTimeout bounds the connect that tells a stale socket file from a
// live one.
const staleCheckTimeout = time.Second

// maxSocketPath returns the longest socket path Go binds: sun_path holds 108
// bytes on Linux and 104 on macOS, the terminating NUL included.
func maxSocketPath() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}

// tmpPrefix starts the temporary name a socket is bound under, in the same
// directory; eight random characters follow.
const tmpPrefix = ".kest-"

// unixSocket is the file of a listening unix socket.
type unixSocket struct {
	path string
	info fs.FileInfo // to remove the file only while it is still this socket
}

// listenUnix binds a unix socket at path with mode. It binds under a
// temporary name in the same directory, sets the mode, and links the socket
// into place: nobody can connect before the mode applies, and a file that
// appears at path meanwhile is not replaced. A stale socket at path, one that
// nobody listens on, is removed first.
func listenUnix(ctx context.Context, path string, mode fs.FileMode) (net.Listener, *unixSocket, error) {
	tmp := filepath.Join(filepath.Dir(path), tmpPrefix+rand.Text()[:8])
	switch limit := maxSocketPath(); {
	case len(path) > limit:
		return nil, nil, fmt.Errorf("socket path %s is %d bytes long; the OS takes at most %d", path, len(path), limit)
	case len(tmp) > limit:
		return nil, nil, fmt.Errorf("the directory of socket path %s is too long: kest binds a %d-byte temporary "+
			"name there first, and the OS takes at most %d bytes", path, len(tmp)-len(filepath.Dir(path)), limit)
	}
	if err := removeStale(ctx, path); err != nil {
		return nil, nil, err
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", tmp)
	if err != nil {
		return nil, nil, err
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false) // tmp goes below, path in remove
	}
	defer func() { _ = os.Remove(tmp) }()

	err = os.Chmod(tmp, mode)
	if err == nil {
		err = os.Link(tmp, path)
		if errors.Is(err, fs.ErrExist) {
			err = fmt.Errorf("%s appeared while kest was binding it: %w", path, err)
		}
	}
	if err != nil {
		_ = ln.Close()
		return nil, nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, nil, err
	}
	return ln, &unixSocket{path: path, info: info}, nil
}

// remove deletes the socket file, unless something else has replaced it.
func (s *unixSocket) remove() error {
	info, err := os.Lstat(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !os.SameFile(info, s.info):
		return nil
	}
	return os.Remove(s.path)
}

// removeStale removes the socket file at path if nobody listens on it. A
// socket someone listens on, or a file that isn't a socket, is an error.
func removeStale(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode().Type() != fs.ModeSocket:
		return fmt.Errorf("%s exists and is not a socket", path)
	}

	dctx, cancel := context.WithTimeout(ctx, staleCheckTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dctx, "unix", path)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("another process listens on %s", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("check the socket file %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove the stale socket file: %w", err)
	}
	return nil
}
