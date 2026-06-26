package daemon

import (
	"bytes"
	"sync"
)

// LogTail tees the process's log stream. It is an io.Writer meant to sit
// behind the standard logger (via log.SetOutput(io.MultiWriter(os.Stderr,
// tail))): every write still reaches stderr, and a copy is split into lines,
// retained in a bounded ring buffer, and fanned out to live subscribers.
//
// Why tee in-process rather than read a log file or journald: dzd logs only
// to stderr (there is no log file), and the production container runs
// unprivileged — it can't read the docker socket or the systemd journal. So
// the deployment-agnostic way to expose "the real system log" to a platform
// admin is to capture exactly what the daemon emits, from the moment the tee
// is installed onward. The ring buffer gives a new viewer recent history;
// subscribers get every line live.
type LogTail struct {
	mu sync.Mutex
	// buf is a circular buffer of the most recent `cap(buf)` lines. start
	// is the index of the oldest retained line; count is how many are live.
	buf     []string
	start   int
	count   int
	partial []byte // bytes received since the last newline
	subs    map[int]chan string
	nextID  int
}

// maxPartial caps the unflushed tail so a pathological newline-less write
// can't grow memory without bound; once exceeded we emit what we have as a
// line and reset.
const maxPartial = 64 * 1024

// NewLogTail returns a LogTail that retains the last `size` log lines for
// backfill (default 2000 when size <= 0).
func NewLogTail(size int) *LogTail {
	if size <= 0 {
		size = 2000
	}
	return &LogTail{
		buf:  make([]string, size),
		subs: make(map[int]chan string),
	}
}

// Write implements io.Writer. It never returns an error and always reports
// the full length consumed, so it's safe behind io.MultiWriter — a stuck or
// slow log viewer must never break the daemon's own logging.
func (lt *LogTail) Write(p []byte) (int, error) {
	lt.mu.Lock()
	lt.partial = append(lt.partial, p...)
	for {
		i := bytes.IndexByte(lt.partial, '\n')
		if i < 0 {
			break
		}
		lt.emitLocked(string(lt.partial[:i]))
		// Reslice past the newline; periodically compact so the backing
		// array doesn't grow unbounded across many partial writes.
		lt.partial = lt.partial[i+1:]
	}
	if len(lt.partial) > maxPartial {
		lt.emitLocked(string(lt.partial))
		lt.partial = lt.partial[:0]
	}
	// Compact the leftover tail into a fresh small slice so we don't pin a
	// large backing array between writes.
	if len(lt.partial) == 0 && cap(lt.partial) > maxPartial {
		lt.partial = nil
	}
	lt.mu.Unlock()
	return len(p), nil
}

// emitLocked stores a line in the ring and fans it out. Caller holds mu.
func (lt *LogTail) emitLocked(line string) {
	idx := (lt.start + lt.count) % len(lt.buf)
	if lt.count < len(lt.buf) {
		lt.buf[idx] = line
		lt.count++
	} else {
		// Full: overwrite the oldest and advance start.
		lt.buf[lt.start] = line
		lt.start = (lt.start + 1) % len(lt.buf)
	}
	for _, ch := range lt.subs {
		select {
		case ch <- line:
		default:
			// Subscriber is behind its buffer — drop this line for it
			// rather than block every other writer/subscriber. A live
			// tail tolerates an occasional gap.
		}
	}
}

// Snapshot returns the retained lines oldest-first. When max > 0 only the
// most recent max lines are returned.
func (lt *LogTail) Snapshot(max int) []string {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	out := make([]string, 0, lt.count)
	for i := 0; i < lt.count; i++ {
		out = append(out, lt.buf[(lt.start+i)%len(lt.buf)])
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out
}

// Subscribe registers a live feed of new log lines. The returned cancel
// closes and unregisters the channel; call it (e.g. via defer) when the
// consumer is done. The channel is buffered; if the consumer can't keep up,
// lines are dropped for it (see emitLocked) rather than blocking the logger.
func (lt *LogTail) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 256)
	lt.mu.Lock()
	id := lt.nextID
	lt.nextID++
	lt.subs[id] = ch
	lt.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			lt.mu.Lock()
			delete(lt.subs, id)
			close(ch)
			lt.mu.Unlock()
		})
	}
	return ch, cancel
}
