package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketNeverInSharedTmp(t *testing.T) {
	state := t.TempDir()
	t.Setenv("OMAMESSAGES_DIR", state)
	t.Setenv("XDG_RUNTIME_DIR", "")
	p, err := socketPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) == filepath.Clean(os.TempDir()) {
		t.Fatalf("socket %s is in the shared temp dir", p)
	}
	if want := filepath.Join(state, "run", socketName); p != want {
		t.Errorf("socket = %s; want %s", p, want)
	}
	info, err := os.Stat(filepath.Dir(p))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("fallback dir mode = %v, %v; want 0700", info.Mode().Perm(), err)
	}
}

func TestRuntimeDirRejectsUnsafeXDG(t *testing.T) {
	state := t.TempDir()
	t.Setenv("OMAMESSAGES_DIR", state)
	fallback := filepath.Join(state, "run")

	open := t.TempDir()
	os.Chmod(open, 0o777)
	t.Setenv("XDG_RUNTIME_DIR", open)
	if d, err := runtimeDir(); err != nil || d != fallback {
		t.Errorf("world-writable XDG_RUNTIME_DIR: got %s, %v; want the private fallback", d, err)
	}

	real := t.TempDir()
	os.Chmod(real, 0o700)
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(real, link)
	t.Setenv("XDG_RUNTIME_DIR", link)
	if d, err := runtimeDir(); err != nil || d != fallback {
		t.Errorf("symlinked XDG_RUNTIME_DIR: got %s, %v; want the private fallback", d, err)
	}

	os.Chmod(real, 0o700)
	t.Setenv("XDG_RUNTIME_DIR", real)
	if d, err := runtimeDir(); err != nil || d != real {
		t.Errorf("private XDG_RUNTIME_DIR: got %s, %v; want it used", d, err)
	}
}

func TestListenIsOwnerOnlyAndChecksPeers(t *testing.T) {
	rt := t.TempDir()
	os.Chmod(rt, 0o700)
	t.Setenv("XDG_RUNTIME_DIR", rt)
	ln, err := listen()
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	p, _ := socketPath()
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Errorf("socket mode = %v, %v; want no access for group or others", info.Mode().Perm(), err)
	}

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	c, err := net.Dial("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := <-accepted
	defer s.Close()
	for name, conn := range map[string]net.Conn{"client side": c, "daemon side": s} {
		uid, err := peerUID(conn)
		if err != nil || uid != os.Getuid() {
			t.Errorf("%s: peer uid = %d, %v; want %d", name, uid, err, os.Getuid())
		}
		if !samePeer(conn) {
			t.Errorf("%s: samePeer false for our own process", name)
		}
	}

	// A second daemon sees the first and bows out.
	if _, err := listen(); err != errAlreadyRunning {
		t.Errorf("second listen: %v; want errAlreadyRunning", err)
	}
}

func TestCheckPrivateDir(t *testing.T) {
	ok := t.TempDir()
	os.Chmod(ok, 0o700)
	if err := checkPrivateDir(ok); err != nil {
		t.Errorf("0700 dir: %v", err)
	}
	gw := t.TempDir()
	os.Chmod(gw, 0o770)
	if err := checkPrivateDir(gw); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Errorf("group-writable dir: %v; want rejected", err)
	}
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, nil, 0o600)
	if err := checkPrivateDir(f); err == nil {
		t.Errorf("a file accepted as a directory")
	}
}
