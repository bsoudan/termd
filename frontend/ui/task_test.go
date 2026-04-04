package ui

import (
	"context"
	"testing"
	"time"

	"nxtermd/pkg/tui"
)

// Core TaskRunner and Handle tests are in pkg/tui/task_test.go.
// These tests cover the nxtermd-specific TermdHandle.Request method
// which routes through Handle.Send → TaskSendMsg → Deliver.

func TestTermdHandleRequest(t *testing.T) {
	type testReq struct{ Name string }
	type testResp struct{ Value int }

	runner := tui.NewTaskRunner()

	var got any
	var gotErr error
	done := make(chan struct{})

	runner.Run(func(h *tui.Handle) {
		th := &TermdHandle{Handle: h}
		got, gotErr = th.Request(testReq{Name: "hello"})
		close(done)
	})

	// DriveOne reads the taskSendMsg and calls HandleMsg (which produces
	// a cmd for TaskSendMsg but doesn't execute it). We need to get the
	// cmd from HandleMsg to extract the TaskSendMsg.
	raw := runner.DriveOne()
	cmd := runner.HandleMsg(raw)

	// HandleMsg returns a cmd that produces TaskSendMsg.
	if cmd == nil {
		t.Fatal("expected non-nil cmd for send message")
	}
	result := cmd()
	tsm, ok := result.(tui.TaskSendMsg)
	if !ok {
		t.Fatalf("expected TaskSendMsg, got %T", result)
	}

	// Simulate the app processing the request on the bubbletea goroutine.
	req, ok := tsm.Payload.(testReq)
	if !ok || req.Name != "hello" {
		t.Fatalf("unexpected payload: %+v", tsm.Payload)
	}
	// Deliver the response (as the app would after server responds).
	runner.Deliver(tsm.TaskID, testResp{Value: 42})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
	resp, ok := got.(testResp)
	if !ok {
		t.Fatalf("expected testResp, got %T", got)
	}
	if resp.Value != 42 {
		t.Fatalf("expected 42, got %d", resp.Value)
	}
}

func TestTermdHandleRequestCancelled(t *testing.T) {
	runner := tui.NewTaskRunner()

	var gotErr error
	done := make(chan struct{})

	id := runner.Run(func(h *tui.Handle) {
		th := &TermdHandle{Handle: h}
		_, gotErr = th.Request("waiting forever")
		close(done)
	})

	// Drive the Send message.
	runner.DriveOne()

	// Cancel the task instead of delivering a response.
	runner.Cancel(id)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}

	if gotErr != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", gotErr)
	}
}
