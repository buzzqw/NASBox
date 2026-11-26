package transfer

import (
	"sync"
	"time"
)

// chunkProgress adapts chunk-level byte callbacks to the same Progress contract
// used by the rsync backend. Callbacks are serialized and invoked without the
// mutex so UI code cannot deadlock the transfer goroutine.
type chunkProgress struct {
	mu         sync.Mutex
	handler    ProgressHandler
	upload     bool
	total      int
	totalBytes int64
	bytes      int64
	done       int
	file       string
	started    time.Time
	finished   bool
}

func newChunkProgress(handler ProgressHandler, upload bool, total int, totalBytes int64) *chunkProgress {
	return &chunkProgress{
		handler: handler, upload: upload, total: total,
		totalBytes: totalBytes, started: time.Now(),
	}
}

func (p *chunkProgress) setFile(path string) {
	p.mu.Lock()
	p.file = path
	p.mu.Unlock()
	p.emit()
}

func (p *chunkProgress) add(delta int64) {
	if delta <= 0 {
		return
	}
	p.mu.Lock()
	p.bytes += delta
	p.mu.Unlock()
	p.emit()
}

func (p *chunkProgress) complete() {
	p.mu.Lock()
	if p.done < p.total {
		p.done++
	}
	p.mu.Unlock()
	p.emit()
}

func (p *chunkProgress) finish(err error) {
	p.mu.Lock()
	if p.finished {
		p.mu.Unlock()
		return
	}
	p.finished = true
	p.mu.Unlock()
	p.emit(err)
}

func (p *chunkProgress) emit(resultErr ...error) {
	p.mu.Lock()
	if p.handler == nil {
		p.mu.Unlock()
		return
	}
	percent := 0
	if p.finished && p.done == p.total {
		percent = 100
	} else if p.totalBytes > 0 {
		percent = int(p.bytes * 100 / p.totalBytes)
		if percent > 100 {
			percent = 100
		}
	} else if p.total > 0 {
		percent = p.done * 100 / p.total
	}
	rate := ""
	elapsed := time.Since(p.started).Seconds()
	if elapsed > 0 && p.bytes > 0 {
		rate = FormatRate(float64(p.bytes) / elapsed)
	}
	state := Progress{
		Upload: p.upload, File: p.file, Percent: percent, Bytes: p.bytes,
		Rate: rate, Done: p.done, Total: p.total, TotalBytes: p.totalBytes,
		Finished: p.finished,
	}
	if len(resultErr) > 0 {
		state.Err = resultErr[0]
	}
	handler := p.handler
	p.mu.Unlock()
	handler(state)
}
