//go:build fake

package main

import "omarchy-omamessages/core"

// Test-only commands, compiled in with `go build -tags fake`.
//
//	panic <service>   make that service's code panic, to show that only it
//	                  goes to "error" and the daemon and other services go on.

func init() {
	extraCommands["panic"] = func(hub *Hub, req Request) Response {
		err := hub.Do(core.ProviderID(req.Str("provider")), func(core.Provider) error {
			panic("requested with the panic command")
		})
		return Response{Error: err.Error()}
	}
}

func extraCLI(cmd string, args []string, req *Request) bool {
	if cmd != "panic" || len(args) != 1 {
		return false
	}
	req.Args["provider"] = args[0]
	return true
}
