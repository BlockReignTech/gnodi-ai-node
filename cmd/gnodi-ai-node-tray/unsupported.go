//go:build !windows

// A tray icon is a Windows feature here. The daemon itself runs everywhere --
// this package exists so `go build ./...` and `go vet ./...` have something to
// compile on the platforms it does not target, rather than failing with "build
// constraints exclude all Go files".
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr,
		"gnodi-ai-node-tray is Windows-only. Run gnodi-ai-node directly on this platform.")
	os.Exit(1)
}
