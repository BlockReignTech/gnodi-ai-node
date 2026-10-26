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
		// Rounded, not truncated. A card never reports the size on its box:
		// an 8GB RTX 5060 Ti reports 8151MiB because the driver reserves the
		// rest, and truncating 7.96 gives 7 -- below every threshold in the
		// catalog, so a perfectly capable card is told nothing fits it. The
		// thresholds are written against marketed sizes, so this has to
		// recover the marketed size.
		if gb := (mib + 512) / 1024; gb > best {
			best = gb
		}
	}
	return best
}

// vramReserveFraction and vramReserveFloor hold memory back from the warmed set.
//
// The card's size is not the budget. A model's resident footprint grows with
// the context it is serving, the allocator fragments, and on a desktop the
// compositor, the browser and anything else with a window are already holding
// VRAM that the engine's own accounting cannot see. Spending the whole card
// means the first long prompt is the one that fails.
const (
	vramReserveFraction = 0.15
	vramReserveFloor    = int64(1536) << 20 // 1.5 GiB
)

// BudgetBytes is how much card memory the warmed set may occupy.
//
// This exists because the catalog's own threshold is per model and not
// cumulative: every model at or under the card's size is eligible, and with
// weights pinned resident they are all held at once. On a 15GB card that
// selects three models totalling about 20GB — which does not fail cleanly, it
// thrashes. The budget is what turns "every model that fits alone" into "the
// models that fit together".
func BudgetBytes(vramGb int) int64 {
	if vramGb <= 0 {
		return 0 // unknown card: no budget to enforce, and nothing to enforce it on
	}
	total := int64(vramGb) << 30
	reserve := int64(float64(total) * vramReserveFraction)
	if reserve < vramReserveFloor {
		reserve = vramReserveFloor
	}
	if reserve >= total {
		return 0
	}
	return total - reserve
}
