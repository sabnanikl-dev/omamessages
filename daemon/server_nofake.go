//go:build !fake

package main

// extraCLI parses commands that exist only in tagged builds; there are none
// in a normal build.
func extraCLI(cmd string, args []string, req *Request) bool { return false }
