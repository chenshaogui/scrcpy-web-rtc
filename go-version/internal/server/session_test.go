package server

import (
	"testing"
	"time"
)

func TestEnqueueControlWaitsWhenQueueIsFull(t *testing.T) {
	s := &Session{
		controlChain: make(chan func(), 1),
		done:         make(chan struct{}),
	}
	s.controlChain <- func() {}

	enqueued := make(chan struct{})
	go func() {
		s.enqueueControl(func() {})
		close(enqueued)
	}()

	select {
	case <-enqueued:
		t.Fatal("enqueueControl returned while the queue was full")
	case <-time.After(20 * time.Millisecond):
	}

	<-s.controlChain
	select {
	case <-enqueued:
	case <-time.After(time.Second):
		t.Fatal("enqueueControl did not resume after queue space became available")
	}
}
