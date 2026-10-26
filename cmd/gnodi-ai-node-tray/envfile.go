// Config loading, kept out of the Windows-only file so it can be tested.
//
// This has to agree exactly with the PowerShell launcher's parser: a node
// started from the tray and one started from a terminal must be configured
// identically, or the difference shows up as a node that behaves differently
// depending on how it was launched.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// envName is the config file the tray and the PowerShell launcher both read.
const envName = "node.env"

// loadEnvFile puts KEY=value lines into this process's environment.
//
// The same file the PowerShell launcher reads, parsed the same way, so a node
// started from the tray and one started from a terminal are configured
// identically. Existing variables win, so a shell can still override.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no %s beside the executable: %w", envName, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, strings.TrimSpace(value)); err != nil {
			return err
		}
	}
	return sc.Err()
}
