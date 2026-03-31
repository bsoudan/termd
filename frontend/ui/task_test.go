package ui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// driveOneTimeout reads one message from the runner with a timeout.
func driveOneTimeout(t *testing.T, r *TaskRunner, timeout time.Duration) taskMsg {
	t.Helper()
	select {
	case msg := <-r.fromTasks:
		r.HandleMsg(msg)
		return msg
	case <-time.After(timeout):
		t.Fatal("timed out waiting for task message")
		return nil
	}
}

// waitDone waits for a channel to close with a timeout.
func waitDone(t *testing.T, ch <-chan struct{}, timeout time.Duration) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for done")
	}
}

func TestTaskRequest(t *testing.T) {
	type testReq struct{ Name string }
	type testResp struct{ Value int }

	runner := NewTaskRunner(func(msg any, reply ReplyFunc) {
		if req, ok := msg.(testReq); ok && req.Name == "hello" {
			reply(testResp{Value: 42})
		}
	})

	var got any
	var gotErr error
	done := make(chan struct{})
	runner.Run(func(h *Handle) {
		got, gotErr = h.Request(testReq{Name: "hello"})
		close(done)
	})

	driveOneTimeout(t, runner, time.Second)
	waitDone(t, done, time.Second)

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

func TestTaskRequestError(t *testing.T) {
	type errResp struct{ Error bool; Message string }

	runner := NewTaskRunner(func(msg any, reply ReplyFunc) {
		reply(errResp{Error: true, Message: "bad request"})
	})

	var got any
	done := make(chan struct{})
	runner.Run(func(h *Handle) {
		got, _ = h.Request("anything")
		close(done)
	})

	driveOneTimeout(t, runner, time.Second)
	waitDone(t, done, time.Second)

	resp := got.(errResp)
	if !resp.Error || resp.Message != "bad request" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestWaitForDeliverHandled(t *testing.T) {
	runner := NewTaskRunner(nil)

	var got any
	done := make(chan struct{})
	runner.Run(func(h *Handle) {
		got, _ = h.WaitFor(IsKeyPress)
		close(done)
	})

	driveOneTimeout(t, runner, time.Second) // taskWaitForMsg

	handled := runner.CheckFilters(tea.KeyPressMsg{})
	if !handled {
		t.Fatal("expected handled=true for key press")
	}

	waitDone(t, done, time.Second)
	if got == nil {
		t.Fatal("expected non-nil message")
	}
	if _, ok := got.(tea.KeyPressMsg); !ok {
		t.Fatalf("expected KeyPressMsg, got %T", got)
	}
}

func TestWaitForDeliverNotHandled(t *testing.T) {
	runner := NewTaskRunner(nil)

	type reconnected struct{}

	var got any
	done := make(chan struct{})
	runner.Run(func(h *Handle) {
		got, _ = h.WaitFor(func(msg any) (bool, bool) {
			_, ok := msg.(reconnected)
			return ok, false
		})
		close(done)
	})

	driveOneTimeout(t, runner, time.Second)

	handled := runner.CheckFilters(reconnected{})
	if handled {
		t.Fatal("expected handled=false for reconnected")
	}

	waitDone(t, done, time.Second)
	if got == nil {
		t.Fatal("expected non-nil message")
	}
}

func TestWaitForSkipsNonMatching(t *testing.T) {
	runner := NewTaskRunner(nil)

	type target struct{}
	type noise struct{}

	var got any
	done := make(chan struct{})
	runner.Run(func(h *Handle) {
		got, _ = h.WaitFor(func(msg any) (bool, bool) {
			_, ok := msg.(target)
			return ok, ok
		})
		close(done)
	})

	driveOneTimeout(t, runner, time.Second)

	// Non-matching message — should not deliver.
	handled := runner.CheckFilters(noise{})
	if handled {
		t.Fatal("noise should not be handled")
	}
	select {
	case <-done:
		t.Fatal("task should still be waiting")
	default:
	}

	// Matching message — should deliver.
	runner.CheckFilters(target{})
	waitDone(t, done, time.Second)
	if got == nil {
		t.Fatal("expected target message")
	}
}

func TestWaitForFilterCleared(t *testing.T) {
	runner := NewTaskRunner(nil)

	done := make(chan struct{})
	runner.Run(func(h *Handle) {
		h.WaitFor(IsKeyPress)
		close(done)
	})

	driveOneTimeout(t, runner, time.Second)

	// First key — delivered, filter cleared.
	runner.CheckFilters(tea.KeyPressMsg{})
	waitDone(t, done, time.Second)

	// Second key — no filter active, should not be handled.
	handled := runner.CheckFilters(tea.KeyPressMsg{})
	if handled {
		t.Fatal("filter should have been cleared after delivery")
	}
}

func TestCancelUnblocksRequest(t *testing.T) {
	// requestFn that never replies.
	runner := NewTaskRunner(func(msg any, reply ReplyFunc) {})

	var gotErr error
	done := make(chan struct{})
	id := runner.Run(func(h *Handle) {
		_, gotErr = h.Request("waiting forever")
		close(done)
	})

	driveOneTimeout(t, runner, time.Second) // taskRequestMsg
	runner.Cancel(id)
	waitDone(t, done, time.Second)

	if gotErr != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", gotErr)
	}
}

func TestCancelUnblocksWaitFor(t *testing.T) {
	runner := NewTaskRunner(nil)

	var gotErr error
	done := make(chan struct{})
	id := runner.Run(func(h *Handle) {
		_, gotErr = h.WaitFor(func(msg any) (bool, bool) {
			return false, false // never matches
		})
		close(done)
	})

	driveOneTimeout(t, runner, time.Second) // taskWaitForMsg
	runner.Cancel(id)
	waitDone(t, done, time.Second)

	if gotErr != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", gotErr)
	}
}

func TestPushPopLayer(t *testing.T) {
	runner := NewTaskRunner(nil)

	overlay := &Overlay{Title: "test"}
	done := make(chan struct{})
	runner.Run(func(h *Handle) {
		h.PushLayer(overlay)
		h.PopLayer(overlay)
		close(done)
	})

	msg1 := driveOneTimeout(t, runner, time.Second)
	if _, ok := msg1.(taskPushLayerMsg); !ok {
		t.Fatalf("expected taskPushLayerMsg, got %T", msg1)
	}

	msg2 := driveOneTimeout(t, runner, time.Second)
	if _, ok := msg2.(taskPopLayerMsg); !ok {
		t.Fatalf("expected taskPopLayerMsg, got %T", msg2)
	}

	waitDone(t, done, time.Second)
}

func TestMultiStepTask(t *testing.T) {
	type req1 struct{}
	type resp1 struct{ Step int }
	type signal struct{}
	type req2 struct{}
	type resp2 struct{ Step int }

	step := 0
	runner := NewTaskRunner(func(msg any, reply ReplyFunc) {
		switch msg.(type) {
		case req1:
			reply(resp1{Step: 1})
		case req2:
			reply(resp2{Step: 3})
		}
	})

	done := make(chan struct{})
	runner.Run(func(h *Handle) {
		// Step 1: request
		got, _ := h.Request(req1{})
		if got.(resp1).Step != 1 {
			t.Errorf("step 1: expected 1, got %+v", got)
		}
		step = 1

		// Step 2: wait for signal
		got, _ = h.WaitFor(func(msg any) (bool, bool) {
			_, ok := msg.(signal)
			return ok, false
		})
		step = 2

		// Step 3: another request
		got, _ = h.Request(req2{})
		if got.(resp2).Step != 3 {
			t.Errorf("step 3: expected 3, got %+v", got)
		}
		step = 3
		close(done)
	})

	// Drive step 1
	driveOneTimeout(t, runner, time.Second) // request
	time.Sleep(10 * time.Millisecond)       // let goroutine advance
	if step != 1 {
		t.Fatalf("expected step 1, got %d", step)
	}

	// Drive step 2
	driveOneTimeout(t, runner, time.Second) // waitFor registration
	runner.CheckFilters(signal{})
	time.Sleep(10 * time.Millisecond)
	if step != 2 {
		t.Fatalf("expected step 2, got %d", step)
	}

	// Drive step 3
	driveOneTimeout(t, runner, time.Second) // request
	waitDone(t, done, time.Second)
	if step != 3 {
		t.Fatalf("expected step 3, got %d", step)
	}
}

func TestTaskDone(t *testing.T) {
	runner := NewTaskRunner(nil)

	id := runner.Run(func(h *Handle) {
		// Immediately return.
	})

	// Task should send taskDoneMsg.
	msg := driveOneTimeout(t, runner, time.Second)
	if dm, ok := msg.(taskDoneMsg); !ok {
		t.Fatalf("expected taskDoneMsg, got %T", msg)
	} else if dm.taskID != id {
		t.Fatalf("expected task ID %d, got %d", id, dm.taskID)
	}

	// Task should be cleaned up.
	runner.mu.Lock()
	_, exists := runner.tasks[id]
	runner.mu.Unlock()
	if exists {
		t.Fatal("task should have been removed from tasks map")
	}
}

func TestTaskPanicRecovery(t *testing.T) {
	runner := NewTaskRunner(nil)

	id := runner.Run(func(h *Handle) {
		panic("test panic")
	})

	// Should still get taskDoneMsg despite the panic.
	msg := driveOneTimeout(t, runner, time.Second)
	if dm, ok := msg.(taskDoneMsg); !ok {
		t.Fatalf("expected taskDoneMsg, got %T", msg)
	} else if dm.taskID != id {
		t.Fatalf("expected task ID %d, got %d", id, dm.taskID)
	}

	runner.mu.Lock()
	_, exists := runner.tasks[id]
	runner.mu.Unlock()
	if exists {
		t.Fatal("task should have been removed after panic")
	}
}
