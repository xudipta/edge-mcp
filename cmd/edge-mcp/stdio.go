package main

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// The go-sdk cancels in-flight requests as soon as stdin hits EOF, so piped
// input ("printf ... | edge-mcp") loses every response. drainTracker holds the
// EOF back until each request read from stdin has been answered on stdout.
type drainTracker struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending int
}

func newDrainTracker() *drainTracker {
	d := &drainTracker{}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func (d *drainTracker) add(n int) {
	d.mu.Lock()
	d.pending += n
	d.cond.Broadcast()
	d.mu.Unlock()
}

// wait blocks until nothing is pending, or until timeout elapses.
func (d *drainTracker) wait(timeout time.Duration) {
	t := time.AfterFunc(timeout, func() {
		d.mu.Lock()
		d.pending = 0
		d.cond.Broadcast()
		d.mu.Unlock()
	})
	defer t.Stop()
	d.mu.Lock()
	defer d.mu.Unlock()
	for d.pending > 0 {
		d.cond.Wait()
	}
}

type envelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

// call reports whether a JSON-RPC line is a request that expects a response.
func isCall(line []byte) bool {
	var e envelope
	return json.Unmarshal(line, &e) == nil && e.Method != "" && len(e.ID) > 0 && string(e.ID) != "null"
}

// isResponse reports whether a JSON-RPC line answers a request.
func isResponse(line []byte) bool {
	var e envelope
	return json.Unmarshal(line, &e) == nil && e.Method == "" && len(e.ID) > 0
}

// lineScanner invokes fn for each complete newline-terminated line passed through it.
type lineScanner struct {
	buf []byte
	fn  func(line []byte)
}

func (s *lineScanner) feed(p []byte) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			return
		}
		if line := bytes.TrimSpace(s.buf[:i]); len(line) > 0 {
			s.fn(line)
		}
		s.buf = s.buf[i+1:]
	}
}

type drainReader struct {
	r       io.ReadCloser
	d       *drainTracker
	timeout time.Duration
	scan    lineScanner
}

func (r *drainReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.scan.feed(p[:n])
	if err == io.EOF {
		// A final request without a trailing newline is still a request.
		r.scan.feed([]byte("\n"))
		r.d.wait(r.timeout)
	}
	return n, err
}

func (r *drainReader) Close() error { return r.r.Close() }

type drainWriter struct {
	w    io.WriteCloser
	mu   sync.Mutex
	scan lineScanner
}

func (w *drainWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	w.mu.Lock()
	w.scan.feed(p[:n])
	w.mu.Unlock()
	return n, err
}

func (w *drainWriter) Close() error { return w.w.Close() }

// drainingStdio wraps stdin/stdout so that EOF on stdin waits (up to timeout)
// for the responses to requests already received.
func drainingStdio(in io.ReadCloser, out io.WriteCloser, timeout time.Duration) (io.ReadCloser, io.WriteCloser) {
	d := newDrainTracker()
	r := &drainReader{r: in, d: d, timeout: timeout}
	r.scan.fn = func(line []byte) {
		if isCall(line) {
			d.add(1)
		}
	}
	w := &drainWriter{w: out}
	w.scan.fn = func(line []byte) {
		if isResponse(line) {
			d.add(-1)
		}
	}
	return r, w
}
