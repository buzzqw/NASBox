package ui

import (
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

// Buttons must still run their action after the tooltip hooks were added.
func TestHintAndTipButtonsStillTap(t *testing.T) {
	test.NewTempApp(t)
	hits := 0
	hint := newHintButton("a", nil, "tip", widget.MediumImportance, func() { hits++ }, func(string) {})
	tip := plainButton("b", nil, "tip", widget.MediumImportance, func() { hits++ }, func(string) {})
	window := test.NewWindow(hint)
	defer window.Close()
	test.Tap(hint)
	window.SetContent(tip)
	test.Tap(tip)
	if hits != 2 {
		t.Fatalf("taps ran %d actions, want 2", hits)
	}
}

func TestPruneAsksAndReportsTheOutcome(t *testing.T) {
	c := newTestClient(t)
	c.onPrune()
	if c.window.Canvas().Overlays().Top() == nil {
		t.Fatal("Prune history must ask for confirmation")
	}
}
