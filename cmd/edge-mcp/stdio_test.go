package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

type slowEOF struct{ r io.Reader }

func (s slowEOF) Read(p []byte) (int, error) { return s.r.Read(p) }
func (slowEOF) Close() error                 { return nil }

type nopWC struct{ io.Writer }

func (nopWC) Close() error { return nil }

func TestDrainHoldsEOFUntilResponses(t *testing.T) {
	in := slowEOF{strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")}
	var out strings.Builder
	r, w := drainingStdio(in, nopWC{&out}, 5*time.Second)

	done := make(chan struct{})
	go func() {
		_, _ = io.ReadAll(r) // returns only once EOF is released
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("EOF delivered while a request was unanswered")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("EOF not delivered after the response was written")
	}
}

func TestDrainTimesOut(t *testing.T) {
	in := slowEOF{strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"ping"}` + "\n")}
	r, _ := drainingStdio(in, nopWC{io.Discard}, 100*time.Millisecond)
	start := time.Now()
	_, _ = io.ReadAll(r)
	if time.Since(start) > 2*time.Second {
		t.Fatal("drain did not time out")
	}
}
