package scoring

import "testing"

func TestComputeIsDeterministicAndInRange(t *testing.T) {
	for _, id := range []string{"001234567", "079123456789", "123456789"} {
		first := compute(id)
		if first != compute(id) {
			t.Fatalf("compute(%q) is not deterministic", id)
		}
		if first < 300 || first > 850 {
			t.Fatalf("compute(%q) = %d, outside 300-850", id, first)
		}
	}
}

func TestClassifyBoundaries(t *testing.T) {
	cases := []struct {
		score    int
		band     string
		decision string
	}{
		{850, "EXCELLENT", "APPROVED"},
		{750, "EXCELLENT", "APPROVED"},
		{749, "GOOD", "APPROVED"},
		{670, "GOOD", "APPROVED"},
		{669, "FAIR", "MANUAL_REVIEW"},
		{580, "FAIR", "MANUAL_REVIEW"},
		{579, "POOR", "REJECTED"},
		{300, "POOR", "REJECTED"},
	}
	for _, c := range cases {
		band, decision := classify(c.score)
		if band != c.band || decision != c.decision {
			t.Errorf("classify(%d) = (%s, %s), want (%s, %s)", c.score, band, decision, c.band, c.decision)
		}
	}
}
