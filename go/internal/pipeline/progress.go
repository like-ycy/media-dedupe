package pipeline

import (
	"sync"

	"media-dedupe/internal/model"
	"media-dedupe/internal/progress"
)

// scanProgress publishes complete, ordered snapshots even when workers report
// file failures concurrently. Only fatal failures replace the active stage.
type scanProgress struct {
	mu      sync.Mutex
	last    progress.Event
	onEvent func(progress.Event)
}

func (p *scanProgress) emit(ev progress.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Workers may finish after another worker has already stopped the scan.
	if p.last.Stage == progress.StageError && ev.Stage != progress.StageError {
		return
	}
	if ev.Phase == "running" {
		ev.FoundExact = max(ev.FoundExact, p.last.FoundExact)
		ev.FoundSimilar = max(ev.FoundSimilar, p.last.FoundSimilar)
	}
	p.publishLocked(ev)
}

func (p *scanProgress) recordError(path, kind, message string, count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ev := p.last
	ev.Errors = count
	if kind == model.ErrorFatal {
		ev.Stage = progress.StageError
		ev.Phase = "error"
		ev.Message = message
		ev.CurrentFile = path
	} else if ev.Stage != progress.StageError {
		ev.CurrentFile = path
		if kind == model.ErrorSkipped {
			ev.Message = "已跳过无法处理的文件，扫描继续：" + message
		} else {
			ev.Message = "文件处理警告，扫描继续：" + message
		}
	}
	p.publishLocked(ev)
}

// Called with mu held, including the callback, to preserve delivery order.
func (p *scanProgress) publishLocked(ev progress.Event) {
	ev.Percent = max(ev.Percent, p.last.Percent)
	ev.FilesSeen = max(ev.FilesSeen, p.last.FilesSeen)
	ev.Errors = max(ev.Errors, p.last.Errors)
	p.last = ev
	if p.onEvent != nil {
		p.onEvent(ev)
	}
}
