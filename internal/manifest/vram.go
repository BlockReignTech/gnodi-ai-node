package manifest

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DetectVramGb reports the largest single GPU's memory, in whole GB.
//
// Returns 0 when it cannot tell. The caller must then require an explicit
// VRAM_GB rather than guessing: picking models for a card we could not measure
// would either waste the machine or fail every job with an out-of-memory error.
func DetectVramGb(ctx context.Context) int {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 0
	}

	best := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		mib, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		if gb := mib / 1024; gb > best {
			best = gb
		}
	}
	return best
}
