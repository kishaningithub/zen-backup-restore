package backup

import "time"

// Progress is called periodically during long operations.
type Progress func(files int, bytes int64)

const progressInterval = 200 * time.Millisecond

// progressTracker counts processed files and reports to a Progress callback
// at most once per progressInterval.
type progressTracker struct {
	report       Progress
	files        int
	bytes        int64
	lastReported time.Time
}

func (p *progressTracker) add(size int64) {
	p.files++
	p.bytes += size
	if time.Since(p.lastReported) > progressInterval {
		p.flush()
	}
}

func (p *progressTracker) flush() {
	if p.report != nil {
		p.report(p.files, p.bytes)
	}
	p.lastReported = time.Now()
}
