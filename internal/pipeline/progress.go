// Package pipeline orchestrates scan -> hash -> (embed) -> cluster and reports
// progress for the UI. See PLAN §6 Phase 8.
package pipeline

import "sync"

const maxLogLines = 200

// Progress is a thread-safe view of the running pipeline, polled by the UI via
// GET /api/status.
type Progress struct {
	mu      sync.Mutex
	running bool
	phase   string
	done    int
	total   int
	logs    []string
	errMsg  string
}

// Snapshot is a JSON-serializable copy of the progress state.
type Snapshot struct {
	Running bool     `json:"running"`
	Phase   string   `json:"phase"`
	Done    int      `json:"done"`
	Total   int      `json:"total"`
	Log     []string `json:"log"`
	Error   string   `json:"error"`
}

func NewProgress() *Progress { return &Progress{} }

func (p *Progress) start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = true
	p.phase = ""
	p.done, p.total = 0, 0
	p.logs = nil
	p.errMsg = ""
}

// Begin marks a run as started. RunAll does this itself; callers that launch
// RunAll in a goroutine call Begin first so pollers never see a stale idle
// state between the request returning and the goroutine starting.
func (p *Progress) Begin() { p.start() }

// Running reports whether a run is in progress.
func (p *Progress) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *Progress) setPhase(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = name
	p.done, p.total = 0, 0
	p.appendLog("phase: " + name)
}

func (p *Progress) update(done, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done, p.total = done, total
}

// Logf appends a message to the bounded log.
func (p *Progress) Logf(msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.appendLog(msg)
}

// appendLog assumes the lock is held.
func (p *Progress) appendLog(msg string) {
	p.logs = append(p.logs, msg)
	if len(p.logs) > maxLogLines {
		p.logs = p.logs[len(p.logs)-maxLogLines:]
	}
}

func (p *Progress) finish(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	if err != nil {
		p.errMsg = err.Error()
		p.appendLog("error: " + err.Error())
	} else {
		p.appendLog("done")
	}
}

// Get returns a snapshot safe to serialize.
func (p *Progress) Get() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	logs := make([]string, len(p.logs))
	copy(logs, p.logs)
	return Snapshot{
		Running: p.running,
		Phase:   p.phase,
		Done:    p.done,
		Total:   p.total,
		Log:     logs,
		Error:   p.errMsg,
	}
}
