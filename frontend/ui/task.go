package ui

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Handle is the synchronous interface given to a task goroutine.
// Methods block until the bubbletea event loop processes the request.
type Handle struct {
	ctx    context.Context
	cancel context.CancelFunc
	outbox chan<- taskMsg
	inbox  chan any
	id     uint64
}

// Context returns the task's context, cancelled when the task is stopped.
func (h *Handle) Context() context.Context { return h.ctx }

// Request sends a protocol request to the server and blocks until the
// response arrives. Returns the response payload, or an error if the
// task context was cancelled.
func (h *Handle) Request(req any) (any, error) {
	if err := h.send(taskRequestMsg{taskID: h.id, req: req}); err != nil {
		return nil, err
	}
	return h.recv()
}

// WaitFor blocks until filter returns deliver=true for an incoming message.
// The filter runs on the bubbletea goroutine for each message:
//   - deliver=true, handled=true: task gets the message, layers don't
//   - deliver=true, handled=false: task gets it, layers also see it
//   - deliver=false, handled=false: not relevant, skip
func (h *Handle) WaitFor(filter func(msg any) (deliver, handled bool)) (any, error) {
	if err := h.send(taskWaitForMsg{taskID: h.id, filter: filter}); err != nil {
		return nil, err
	}
	return h.recv()
}

// PushLayer pushes a layer onto the UI stack.
func (h *Handle) PushLayer(layer Layer) {
	h.send(taskPushLayerMsg{layer: layer})
}

// PopLayer removes a layer from the UI stack.
func (h *Handle) PopLayer(layer Layer) {
	h.send(taskPopLayerMsg{layer: layer})
}

func (h *Handle) send(msg taskMsg) error {
	select {
	case h.outbox <- msg:
		return nil
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}

func (h *Handle) recv() (any, error) {
	select {
	case v := <-h.inbox:
		return v, nil
	case <-h.ctx.Done():
		return nil, h.ctx.Err()
	}
}

// taskMsg is the interface for messages sent from task goroutines to bubbletea.
type taskMsg interface {
	isTaskMsg()
}

type taskRequestMsg struct {
	taskID uint64
	req    any
}

type taskWaitForMsg struct {
	taskID uint64
	filter func(any) (deliver, handled bool)
}

type taskPushLayerMsg struct {
	layer Layer
}

type taskPopLayerMsg struct {
	layer Layer
}

type taskDoneMsg struct {
	taskID uint64
}

func (taskRequestMsg) isTaskMsg()  {}
func (taskWaitForMsg) isTaskMsg()  {}
func (taskPushLayerMsg) isTaskMsg() {}
func (taskPopLayerMsg) isTaskMsg()  {}
func (taskDoneMsg) isTaskMsg()     {}

// taskState tracks a running task's WaitFor filter.
type taskState struct {
	handle *Handle
	filter func(any) (deliver, handled bool)
}

// TaskRunner manages running tasks and bridges them to bubbletea.
type TaskRunner struct {
	requestFn RequestFunc
	fromTasks chan taskMsg
	nextID    uint64
	mu        sync.Mutex // protects tasks map for DriveOne/CheckFilters in tests
	tasks     map[uint64]*taskState
}

// NewTaskRunner creates a TaskRunner with the given request function.
func NewTaskRunner(requestFn RequestFunc) *TaskRunner {
	return &TaskRunner{
		requestFn: requestFn,
		fromTasks: make(chan taskMsg),
		tasks:     make(map[uint64]*taskState),
	}
}

// Run spawns a task goroutine. The function fn receives a Handle for
// synchronous communication with the bubbletea event loop.
func (r *TaskRunner) Run(fn func(*Handle)) uint64 {
	r.nextID++
	id := r.nextID

	ctx, cancel := context.WithCancel(context.Background())
	h := &Handle{
		ctx:    ctx,
		cancel: cancel,
		outbox: r.fromTasks,
		inbox:  make(chan any, 1),
		id:     id,
	}

	r.mu.Lock()
	r.tasks[id] = &taskState{handle: h}
	r.mu.Unlock()

	go func() {
		defer func() {
			if rv := recover(); rv != nil {
				slog.Debug("task panic recovered", "task", id, "panic", rv)
			}
			cancel()
			// Best-effort send; if outbox is closed or blocked, skip.
			select {
			case r.fromTasks <- taskDoneMsg{taskID: id}:
			default:
			}
		}()
		fn(h)
	}()

	return id
}

// Cancel cancels a task by ID.
func (r *TaskRunner) Cancel(id uint64) {
	r.mu.Lock()
	ts, ok := r.tasks[id]
	r.mu.Unlock()
	if ok {
		ts.handle.cancel()
	}
}

// ListenCmd returns a tea.Cmd that blocks on the task channel.
// Model should call this from Init and after each taskMsg delivery.
func (r *TaskRunner) ListenCmd() tea.Cmd {
	ch := r.fromTasks
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// CheckFilters runs active WaitFor filters against msg. If a filter
// matches (deliver=true), the message is sent to the task's inbox and
// the filter is cleared. Returns handled=true if the message should
// not be passed to layers.
func (r *TaskRunner) CheckFilters(msg any) (handled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, ts := range r.tasks {
		if ts.filter == nil {
			continue
		}
		deliver, h := ts.filter(msg)
		if deliver {
			ts.filter = nil
			select {
			case ts.handle.inbox <- msg:
			case <-ts.handle.ctx.Done():
			}
			if h {
				return true
			}
		}
	}
	return false
}

// HandleMsg processes a taskMsg from the outbox channel. Returns a tea.Cmd
// for Model to execute (e.g. push/pop layer), or nil.
func (r *TaskRunner) HandleMsg(msg taskMsg) tea.Cmd {
	switch msg := msg.(type) {
	case taskRequestMsg:
		r.mu.Lock()
		ts, ok := r.tasks[msg.taskID]
		r.mu.Unlock()
		if ok && r.requestFn != nil {
			inbox := ts.handle.inbox
			ctx := ts.handle.ctx
			r.requestFn(msg.req, func(payload any) {
				select {
				case inbox <- payload:
				case <-ctx.Done():
				}
			})
		}
		return nil

	case taskWaitForMsg:
		r.mu.Lock()
		if ts, ok := r.tasks[msg.taskID]; ok {
			ts.filter = msg.filter
		}
		r.mu.Unlock()
		return nil

	case taskPushLayerMsg:
		return func() tea.Msg { return PushLayerMsg{Layer: msg.layer} }

	case taskPopLayerMsg:
		layer := msg.layer
		return func() tea.Msg { return popLayerMsg{layer: layer} }

	case taskDoneMsg:
		r.mu.Lock()
		delete(r.tasks, msg.taskID)
		r.mu.Unlock()
		return nil
	}
	return nil
}

// popLayerMsg is sent to remove a specific layer from the stack.
type popLayerMsg struct {
	layer Layer
}

// DriveOne reads one message from the outbox and processes it.
// For testing only — simulates what Model.Update does.
func (r *TaskRunner) DriveOne() taskMsg {
	msg := <-r.fromTasks
	r.HandleMsg(msg)
	return msg
}

// --- Overlay: a simple Layer for task-driven dialogs ---

// Overlay is a simple Layer that displays a bordered dialog.
// Tasks hold a pointer and mutate fields directly between blocking calls.
type Overlay struct {
	Title      string
	Lines      []string
	Help       string
	StatusText string
}

func (o *Overlay) Activate() tea.Cmd { return nil }
func (o *Overlay) Deactivate()       {}

func (o *Overlay) Update(msg tea.Msg) (tea.Msg, tea.Cmd, bool) {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseMsg:
		return nil, nil, true // absorb input
	}
	return nil, nil, false
}

func (o *Overlay) View(width, height int, active bool) []*lipgloss.Layer {
	var lines []string
	if o.Title != "" {
		lines = append(lines, o.Title)
		lines = append(lines, "")
	}
	lines = append(lines, o.Lines...)

	content := strings.Join(lines, "\n")

	overlayW := 50
	dialog := overlayBorder.Width(overlayW).Render(content)

	help := ""
	if o.Help != "" {
		help = statusFaint.Render("• " + o.Help + " •")
	}

	var dialogFull string
	if help != "" {
		dialogLines := strings.Split(dialog, "\n")
		helpPad := (overlayW + overlayBorder.GetHorizontalBorderSize() - lipgloss.Width(help)) / 2
		if helpPad < 0 {
			helpPad = 0
		}
		dialogLines = append(dialogLines, strings.Repeat(" ", helpPad)+help)
		dialogFull = strings.Join(dialogLines, "\n")
	} else {
		dialogFull = dialog
	}

	dialogH := strings.Count(dialogFull, "\n") + 1
	x := (width - overlayW) / 2
	y := (height - dialogH) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	return []*lipgloss.Layer{lipgloss.NewLayer(dialogFull).X(x).Y(y).Z(1)}
}

func (o *Overlay) Status() (string, lipgloss.Style) {
	if o.StatusText != "" {
		return o.StatusText, statusBold
	}
	if o.Title != "" {
		return o.Title, statusBold
	}
	return "", lipgloss.Style{}
}

// IsKeyPress is a WaitFor filter that delivers key press events and consumes them.
func IsKeyPress(msg any) (deliver, handled bool) {
	_, ok := msg.(tea.KeyPressMsg)
	return ok, ok
}

// ShowError sets the overlay to an error state and waits for dismiss.
func ShowError(overlay *Overlay, h *Handle, errMsg string) {
	overlay.Lines = []string{"  Error: " + errMsg, "", "  Press any key to close."}
	overlay.Help = "any key: close"
	overlay.StatusText = "error: " + errMsg
	h.WaitFor(IsKeyPress)
}
