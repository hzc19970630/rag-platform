package search

import "testing"

func TestNeighbourWindowSpansPerSideAroundOutermostAnchors(t *testing.T) {
	// Two anchors in the same version: the span covers perSide chunks on each
	// side of the outermost ones, and asks the database for it in one query.
	low, high := neighbourWindow([]int{5, 3}, 1)
	if low != 2 || high != 6 {
		t.Fatalf("expected the window 2..6, got %d..%d", low, high)
	}
	if low, high := neighbourWindow([]int{7}, 0); low != 7 || high != 7 {
		t.Fatalf("perSide=0 must load only the anchor ordinals, got %d..%d", low, high)
	}
}

func TestWithinWindowUsesPerSideDistance(t *testing.T) {
	selected := []int{5, 9}
	if !withinWindow(6, selected, 1) {
		t.Fatalf("one step from an anchor must be inside a per-side window of 1")
	}
	if !withinWindow(8, selected, 1) {
		t.Fatalf("one step below the second anchor must be inside the window")
	}
	if withinWindow(7, selected, 1) {
		t.Fatalf("a chunk one step from both anchors must still be outside a window of 1")
	}
	if withinWindow(6, selected, 0) {
		t.Fatalf("a per-side window of 0 must only accept the anchors themselves")
	}
}
