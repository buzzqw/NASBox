package causal

import "testing"

func TestParseRejectsInvalid(t *testing.T) {
	for _, value := range []string{"", "abc", "dev:x", "dev:0", "a:1,a:2"} {
		if _, ok := Parse(value); ok {
			t.Fatalf("accepted invalid clock %q", value)
		}
	}
}

func TestMergeDominatesAndIncrement(t *testing.T) {
	a, _ := Parse("pc1:2,pc2:1")
	b, _ := Parse("pc1:1,pc3:4")
	merged := a.Merge(b)
	if merged.Encode() != "pc1:2,pc2:1,pc3:4" {
		t.Fatalf("bad merge: %s", merged.Encode())
	}
	if !merged.Dominates(a) || !merged.Dominates(b) {
		t.Fatal("merge must dominate both inputs")
	}
	if a.Dominates(merged) {
		t.Fatal("input cannot dominate merge")
	}
	next := a.Increment("pc1")
	if next.Counter("pc1") != 3 {
		t.Fatalf("bad increment: %s", next.Encode())
	}
}

func TestTieBreakIsDeterministic(t *testing.T) {
	a, _ := Parse("pc1:2")
	b, _ := Parse("pc2:2")
	if a.CompareWithDevice("pc1", b, "pc2") >= 0 {
		t.Fatal("pc1 should lose the tie-break against pc2")
	}
	if b.CompareWithDevice("pc2", a, "pc1") <= 0 {
		t.Fatal("tie-break must be symmetric")
	}
}
