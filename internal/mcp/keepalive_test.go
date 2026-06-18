package mcp

import (
	"context"
	"errors"
	"testing"
	"time"
)

// These unit tests cover the keepalive decision logic (which notification to
// emit and with what payload). End-to-end delivery through the server's real
// notification routing is guarded by TestMCP_KeepaliveDelivered_Regression in
// the e2e package.

// capturedNotification records one SendNotificationToClient call.
type capturedNotification struct {
	method string
	params map[string]any
}

// fakeNotifier captures notifications instead of sending them, and can be
// configured to fail after a given number of sends.
type fakeNotifier struct {
	sent     []capturedNotification
	failFrom int // if > 0, the Nth send (1-indexed) and onward return an error
}

func (f *fakeNotifier) SendNotificationToClient(_ context.Context, method string, params map[string]any) error {
	f.sent = append(f.sent, capturedNotification{method: method, params: params})
	if f.failFrom > 0 && len(f.sent) >= f.failFrom {
		return errors.New("send failed")
	}
	return nil
}

// TestKeepalive_EmitsProgressWithToken locks in the load-bearing behavior of
// the timeout fix: with a progress token, the keepalive emits
// "notifications/progress" (NOT a log notification) carrying that token.
// If a future change reverts to "notifications/message", this fails.
func TestKeepalive_EmitsProgressWithToken(t *testing.T) {
	f := &fakeNotifier{}
	k := newKeepalive(context.Background(), f, "tok-1")

	k.send("hello")

	if len(f.sent) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(f.sent))
	}
	n := f.sent[0]
	if n.method != "notifications/progress" {
		t.Errorf("method = %q, want notifications/progress (log notifications do not reset client timeouts)", n.method)
	}
	if n.params["progressToken"] != "tok-1" {
		t.Errorf("progressToken = %v, want tok-1", n.params["progressToken"])
	}
	if n.params["message"] != "hello" {
		t.Errorf("message = %v, want hello", n.params["message"])
	}
}

// TestKeepalive_ProgressIncreases verifies the progress value strictly
// increases across sends, as the MCP spec requires.
func TestKeepalive_ProgressIncreases(t *testing.T) {
	f := &fakeNotifier{}
	k := newKeepalive(context.Background(), f, "tok-1")

	k.send("a")
	k.send("b")
	k.send("c")

	if len(f.sent) != 3 {
		t.Fatalf("expected 3 notifications, got %d", len(f.sent))
	}
	var prev float64
	for i, n := range f.sent {
		p, ok := n.params["progress"].(float64)
		if !ok {
			t.Fatalf("send %d: progress not a float64: %T", i, n.params["progress"])
		}
		if i > 0 && p <= prev {
			t.Errorf("send %d: progress %v did not increase from %v", i, p, prev)
		}
		prev = p
	}
}

// TestKeepalive_NoTokenEmitsNothing verifies that without a progress token
// the keepalive sends nothing — there is no token to associate a timeout
// reset with, so a progress notification would be meaningless.
func TestKeepalive_NoTokenEmitsNothing(t *testing.T) {
	f := &fakeNotifier{}
	k := newKeepalive(context.Background(), f, nil)

	k.send("hello")
	k.send("again")

	if len(f.sent) != 0 {
		t.Fatalf("expected no notifications without a token, got %d", len(f.sent))
	}
}

// TestKeepalive_StopsAfterFailure verifies that once a send fails the
// keepalive stops sending (so a broken connection doesn't get hammered), but
// the failure never panics — inference must continue regardless.
func TestKeepalive_StopsAfterFailure(t *testing.T) {
	f := &fakeNotifier{failFrom: 1} // first send fails
	k := newKeepalive(context.Background(), f, "tok-1")

	k.send("first") // fails
	k.send("second")
	k.send("third")

	if len(f.sent) != 1 {
		t.Errorf("expected sending to stop after first failure, got %d sends", len(f.sent))
	}
}

// TestKeepalive_DueRespectsInterval verifies due() gates sends to the flush
// interval and reports false after a failure.
func TestKeepalive_DueRespectsInterval(t *testing.T) {
	f := &fakeNotifier{}
	k := newKeepalive(context.Background(), f, "tok-1")

	// Fresh keepalive: lastFlush is the zero time, so a flush is due.
	if !k.due() {
		t.Error("expected due() true before any send")
	}
	k.send("x")
	if k.due() {
		t.Error("expected due() false immediately after a send")
	}
	// Simulate the interval elapsing.
	k.lastFlush = time.Now().Add(-flushInterval - time.Second)
	if !k.due() {
		t.Error("expected due() true after the interval elapsed")
	}
	// After a failure, never due.
	k.failed = true
	if k.due() {
		t.Error("expected due() false after failure")
	}
}
