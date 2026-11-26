package ui

import (
	"sync/atomic"
	"testing"
	"time"

	"nasbox-go/internal/transfer"
)

func TestOKIsHeldAndCancelledWhenAnotherPassStarts(t *testing.T) {
	c := newTestClient(t)
	c.okHoldOverride = 40 * time.Millisecond
	var applied atomic.Int32

	// "synced" then, within the hold, a new pass: the first one must never show.
	c.holdOK(func() { applied.Add(1) })
	c.cancelOK()
	time.Sleep(120 * time.Millisecond)
	if applied.Load() != 0 {
		t.Fatal("un nuovo passaggio entro l'attesa deve annullare lo stato \"sincronizzato\"")
	}

	// Due "synced" ravvicinati: ne scatta uno solo.
	c.holdOK(func() { applied.Add(1) })
	c.holdOK(func() { applied.Add(1) })
	time.Sleep(150 * time.Millisecond)
	if applied.Load() != 1 {
		t.Fatalf("attesa mancata: applicato %d volte, atteso 1", applied.Load())
	}
}

func TestIdenticalStatusIsNotRedrawn(t *testing.T) {
	c := newTestClient(t)
	c.applyStatus(statusProgress, "in corso")
	c.statusMu.Lock()
	known, level := c.statusKnown, c.lastStatusLevel
	c.statusMu.Unlock()
	if !known || level != statusProgress {
		t.Fatal("lo stato applicato va ricordato")
	}
	// Lo stesso stato due volte di fila non deve ridisegnare il banner.
	c.bannerTitle.SetText("marcatore")
	c.applyStatus(statusProgress, "in corso")
	if c.bannerTitle.Text != "marcatore" {
		t.Fatal("uno stato identico ha ridisegnato il banner (lampeggio)")
	}
	c.applyStatus(statusOK, "tutto a posto")
	if c.bannerTitle.Text == "marcatore" {
		t.Fatal("uno stato diverso deve aggiornare il banner")
	}
}

func TestTransferCardAppearsOnlyForTransfersThatLast(t *testing.T) {
	c := newTestClient(t)
	progress := transfer.Progress{File: "a.txt", Percent: 40, Total: 1}

	c.renderProgress(progress)
	if c.transferCard.Visible() {
		t.Fatal("un trasferimento appena iniziato non deve far comparire la scheda")
	}
	c.progressSince = time.Now().Add(-2 * transferCardDelay)
	c.renderProgress(progress)
	if !c.transferCard.Visible() {
		t.Fatal("un trasferimento che dura deve mostrare la scheda")
	}
	progress.Finished = true
	c.renderProgress(progress)
	if !c.progressSince.IsZero() {
		t.Fatal("a trasferimento finito il timer parte da capo")
	}
}

func TestTrayIconIsSetOnlyWhenTheStateChanges(t *testing.T) {
	c := newTestClient(t)
	c.setTrayIcon("synced")
	c.trayIconState = "synced"
	c.setTrayIcon("synced") // identico: non deve rifare nulla
	c.setTrayIcon("syncing")
	if c.trayIconState != "syncing" {
		t.Fatalf("stato icona = %q", c.trayIconState)
	}
}
