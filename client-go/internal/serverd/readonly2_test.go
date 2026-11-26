package serverd

import (
	"strings"
	"testing"
)

func TestMetricsLinesEmitsNewDiagnosticKeys(t *testing.T) {
	ctx, _ := newTestContext(t)
	ctx.recordPhase("manifest_export", 0)
	ctx.recordChunkHas(4, 3)
	ctx.recordManifestExport(true, false, 2, 120)
	ctx.recordLease("pc-1", 0, 0)
	lines := ctx.metricsLines()
	joined := strings.Join(lines, "\n")
	for _, key := range []string{
		"CHUNK_HAS_HITS=3", "CHUNK_HAS_MISSES=1",
		"CHUNK_HAS_HIT_RATE_PERCENT=75.0",
		"MANIFEST_DELTA_REQUESTS=1", "MANIFEST_DELTA_ROWS=2",
		"PERF_manifest_export_CALLS=1",
		"LOCK_DEVICE_STATS_JSON=", "CHUNK_FSCK_LAST_OK=1",
	} {
		if !strings.Contains(joined, key) {
			t.Errorf("metrica %q mancante in:\n%s", key, joined)
		}
	}
	// The counters survive a new process reading the same state directory.
	fresh := &Context{StateDir: ctx.StateDir}
	again := strings.Join(fresh.metricsLines(), "\n")
	if !strings.Contains(again, "CHUNK_HAS_HITS=3") {
		t.Fatalf("contatori non persistiti su disco:\n%s", again)
	}
}

func TestWebPageHasStatoPage(t *testing.T) {
	for _, fragment := range []string{
		`id="tabs"`, `data-page="stato"`, `id="statusPage"`,
		`id="stDedup"`, `id="stFsck"`, `api("/api/metrics")`,
	} {
		if !strings.Contains(webPage, fragment) {
			t.Errorf("webPage: manca %q", fragment)
		}
	}
}

func TestMetricCountersAreReadOnlyForMetricsVerb(t *testing.T) {
	ctx, _ := newTestContext(t)
	if err := Dispatch(ctx, "--metrics", nil); err != nil {
		t.Fatal(err)
	}
}
