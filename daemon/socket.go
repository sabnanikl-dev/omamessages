package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// The control socket carries what the user types into the panel (codes,
// passwords, pasted cookies), so both ends make sure the other is the same
// user:
//   - it lives only in a directory private to this user: $XDG_RUNTIME_DIR when
//     that is really ours, else <state dir>/run, never a shared /tmp;
//   - the CLI checks the daemon's user id (SO_PEERCRED) before sending, and the
//     daemon checks each caller's before reading.

const socketName = "omarchy-omamessages.sock"

var errForeignPeer = errors.New("the socket belongs to another user; refusing to use it")

// checkPrivateDir makes sure dir is a real directory owned by this user that
// nobody else can write to.
func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by this user", dir)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by other users", dir)
	}
	return nil
}

// runtimeDir is where the socket goes: $XDG_RUNTIME_DIR if it's private to
// this user, otherwise <state dir>/run (created 0700).
func runtimeDir() (string, error) {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		if err := checkPrivateDir(d); err == nil {
			return d, nil
		}
	}
	d := filepath.Join(stateDir(), "run")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	if err := checkPrivateDir(d); err != nil {
		return "", fmt.Errorf("no private place for the control socket: %w", err)
	}
	return d, nil
}

func socketPath() (string, error) {
	d, err := runtimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, socketName), nil
}

// peerUID is the user id of the process at the other end of a unix socket.
func peerUID(conn net.Conn) (int, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return -1, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return -1, err
	}
	if credErr != nil {
		return -1, credErr
	}
	return int(cred.Uid), nil
}

// samePeer reports whether the other end runs as this user.
func samePeer(conn net.Conn) bool {
	uid, err := peerUID(conn)
	return err == nil && uid == os.Getuid()
}
