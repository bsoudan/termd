package layer

import (
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// taskTestLayer is a minimal layer for task tests.
type taskTestLayer struct {
	name string
}

func (l *taskTestLayer) Activate() tea.Cmd                                { return nil }
func (l *taskTestLayer) Deactivate()                                      {}
func (l *taskTestLayer) Update(tea.Msg) (tea.Msg, tea.Cmd, bool)         { return nil, nil, false }
func (l *taskTestLayer) View(int, int, *testRS) []*lipgloss.Layer { return nil }

func TestWaitForDelivers(t *testing.T) {
	r := NewTaskRunner[testRS]()
	gotCh := make(chan any, 1)

	r.Run(func(h *Handle[testRS]) {
		msg, err := h.WaitFor(func(msg any) (bool, bool) {
			_, ok := msg.(testMsg)
			return ok, true
		})
		if err != nil {
			t.Errorf("WaitFor error: %v", err)
			return
		}
		gotCh <- msg
	})

	// Drive the WaitFor registration.
	r.DriveOne()

	// Deliver a message through CheckFilters.
	handled := r.CheckFilters(testMsg("hello"))
	if !handled {
		t.Fatal("expected handled=true")
	}

	select {
	case got := <-gotCh:
		if got != testMsg("hello") {
			t.Fatalf("expected testMsg(hello), got %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task to receive message")
	}
}

func TestWaitForHandledFalse(t *testing.T) {
	r := NewTaskRunner[testRS]()

	r.Run(func(h *Handle[testRS]) {
		h.WaitFor(func(msg any) (bool, bool) {
			_, ok := msg.(testMsg)
			return ok, false // deliver but don't consume
		})
	})

	r.DriveOne()

	handled := r.CheckFilters(testMsg("hello"))
	if handled {
		t.Fatal("expected handled=false when filter returns handled=false")
	}
}

func TestWaitForFilterSkips(t *testing.T) {
	r := NewTaskRunner[testRS]()

	r.Run(func(h *Handle[testRS]) {
		h.WaitFor(func(msg any) (bool, bool) {
			s, ok := msg.(testMsg)
			return ok && string(s) == "target", true
		})
	})

	r.DriveOne()

	// Non-matching message should not be consumed.
	handled := r.CheckFilters(testMsg("other"))
	if handled {
		t.Fatal("non-matching message should not be handled")
	}

	// Matching message should be consumed.
	handled = r.CheckFilters(testMsg("target"))
	if handled != true {
		t.Fatal("matching message should be handled")
	}
}

func TestPushLayerProducesMsg(t *testing.T) {
	r := NewTaskRunner[testRS]()
	layer := &taskTestLayer{name: "overlay"}

	r.Run(func(h *Handle[testRS]) {
		h.PushLayer(layer)
	})

	msg := r.DriveOne()
	if _, ok := msg.(taskPushLayerMsg[testRS]); !ok {
		t.Fatalf("expected taskPushLayerMsg, got %T", msg)
	}

	// HandleMsg should return a cmd that produces PushLayerMsg.
	cmd := r.HandleMsg(msg)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	result := cmd()
	push, ok := result.(PushLayerMsg[testRS])
	if !ok {
		t.Fatalf("expected PushLayerMsg, got %T", result)
	}
	if push.Layer != layer {
		t.Fatal("layer mismatch")
	}
}

func TestPopLayerProducesMsg(t *testing.T) {
	r := NewTaskRunner[testRS]()
	layer := &taskTestLayer{name: "overlay"}

	r.Run(func(h *Handle[testRS]) {
		h.PopLayer(layer)
	})

	msg := r.DriveOne()
	cmd := r.HandleMsg(msg)
	if cmd == nil {
		t.Fatal("expected non-nil cmd")
	}
	result := cmd()
	pop, ok := result.(popLayerMsg[testRS])
	if !ok {
		t.Fatalf("expected popLayerMsg, got %T", result)
	}
	if pop.layer != layer {
		t.Fatal("layer mismatch")
	}
}

func TestCancelStopsTask(t *testing.T) {
	r := NewTaskRunner[testRS]()
	errCh := make(chan error, 1)

	id := r.Run(func(h *Handle[testRS]) {
		_, err := h.WaitFor(func(msg any) (bool, bool) {
			return true, true
		})
		errCh <- err
	})

	r.DriveOne() // process WaitFor registration

	r.Cancel(id)

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from WaitFor after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task to notice cancellation")
	}
}

func TestConcurrentTasks(t *testing.T) {
	r := NewTaskRunner[testRS]()
	var mu sync.Mutex
	results := make(map[string]any)

	r.Run(func(h *Handle[testRS]) {
		msg, _ := h.WaitFor(func(msg any) (bool, bool) {
			s, ok := msg.(testMsg)
			return ok && string(s) == "for-task-1", true
		})
		mu.Lock()
		results["task1"] = msg
		mu.Unlock()
	})

	r.Run(func(h *Handle[testRS]) {
		msg, _ := h.WaitFor(func(msg any) (bool, bool) {
			s, ok := msg.(testMsg)
			return ok && string(s) == "for-task-2", true
		})
		mu.Lock()
		results["task2"] = msg
		mu.Unlock()
	})

	// Drive both WaitFor registrations.
	r.DriveOne()
	r.DriveOne()

	r.CheckFilters(testMsg("for-task-1"))
	r.CheckFilters(testMsg("for-task-2"))

	time.Sleep(10 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if results["task1"] != testMsg("for-task-1") {
		t.Fatalf("task1 got wrong message: %v", results["task1"])
	}
	if results["task2"] != testMsg("for-task-2") {
		t.Fatalf("task2 got wrong message: %v", results["task2"])
	}
}

func TestTaskPanicRecovery(t *testing.T) {
	r := NewTaskRunner[testRS]()

	r.Run(func(h *Handle[testRS]) {
		panic("boom")
	})

	// The task should send a taskDoneMsg despite the panic.
	// DriveOne should not panic.
	msg := r.DriveOne()
	if _, ok := msg.(taskDoneMsg); !ok {
		t.Fatalf("expected taskDoneMsg after panic, got %T", msg)
	}
}

func TestTaskDoneCleanup(t *testing.T) {
	r := NewTaskRunner[testRS]()

	r.Run(func(h *Handle[testRS]) {
		// Task completes immediately.
	})

	msg := r.DriveOne()
	r.HandleMsg(msg)

	r.mu.Lock()
	count := len(r.tasks)
	r.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected 0 tasks after done, got %d", count)
	}
}

func TestIsTaskMsg(t *testing.T) {
	if IsTaskMsg(testMsg("hello")) {
		t.Fatal("testMsg should not be a task message")
	}
	if !IsTaskMsg(taskDoneMsg{taskID: 1}) {
		t.Fatal("taskDoneMsg should be a task message")
	}
}
