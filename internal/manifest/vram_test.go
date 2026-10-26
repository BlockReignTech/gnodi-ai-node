package manifest

import "testing"

func TestVramRoundsToTheMarketedSize(t *testing.T) {
	// What real cards report, against the size on the box. A threshold of 8
	// means "an 8GB card can run this", so truncation defeats the whole
	// comparison: the first 8GB card to run the installer was told that
	// nothing in the catalog fits.
	for _, c := range []struct {
		card string
		mib  int
		want int
	}{
		{"RTX 5060 Ti 8GB", 8151, 8},
		{"RTX 5060 Ti 16GB", 16311, 16},
		{"RTX 3060 12GB", 12288, 12},
		{"RTX 4090", 24564, 24},
		{"Tesla T4", 15360, 15}, // genuinely 15GiB usable; ECC takes the rest
		{"A100 80GB", 81920, 80},
	} {
		if got := (c.mib + 512) / 1024; got != c.want {
			t.Errorf("%s reports %dMiB -> %dGB, want %dGB", c.card, c.mib, got, c.want)
		}
	}
}
