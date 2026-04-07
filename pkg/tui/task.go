package tui

import (
	"context"
	"log/slog"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// Handle is the synchronous interface given to a task goroutine.
// Methods block until the bubbletea event loop processes the request.
type Handle[RS any] struct {
	ctx    context.Context
	cancel context.CancelFunc
	outbox chan<- taskMsg
	inbox  chan any
	id     uint64
}

// Context returns the task's context, cancelled when the task is stopped.
func (h *Handle[RS]) Context() context.Context { return h.ctx }

// WaitFor blocks until filter returns deliver=true for an incoming message.
// The filter runs on the bubbletea goroutine for each message:
//   - deliver=true, handled=true:  task gets the message, layers don't
//   - deliver=true, handled=false: task gets it, layers also see it
//   - deliver=false, handled=false: not relevant, skip
func (h *Handle[RS]) WaitFor(filter func(msg any) (deliver, handled bool)) (any, error) {
	if err := h.send(taskWaitForMsg{taskID: h.id, filter: filter}); err != nil {
		return nil, err
	}
	return h.recv()
}

// Subscribe installs a persistent filter that delivers every matching
// message to the returned channel. Unlike WaitFor, the subscription is
// not cleared after each match — this is the right choice when a task
// needs to process a stream of messages without dropping any.
//
// The returned unsubscribe function must be called when the task is
// done with the stream (typically via defer). The channel is closed by
// the unsubscribe call. Messages are dropped if the channel is full
// (buffer capacity 32) — callers draining in a tight loop shouldn't hit
// this in practice.
func (h *Handle[RS]) Subscribe(filter func(msg any) bool) (<-chan any, func(), error) {
	sub := &subscription{
		filter: filter,
		ch:     make(chan any, 32),
	}
	if err := h.send(taskSubscribeMsg{taskID: h.id, sub: sub}); err != nil {
		return nil, nil, err
	}
	unsub := func() {
		_ = h.send(taskUnsubscribeMsg{taskID: h.id, sub: sub})
	}
	return sub.ch, unsub, nil
}

// Send sends a message to the bubbletea event loop and blocks until
// the app delivers a response via TaskRunner.Deliver. This ensures
// the payload is processed on the bubbletea goroutine, avoiding
// concurrent access to shared state.
func (h *Handle[RS]) Send(msg any) (any, error) {
	if err := h.send(taskSendMsg{taskID: h.id, payload: msg}); err != nil {
		return nil, err
	}
	return h.recv()
}

// PushLayer pushes a layer onto the UI stack.
func (h *Handle[RS]) PushLayer(layer Layer[RS]) {
	h.send(taskPushLayerMsg[RS]{layer: layer})
}

// PopLayer removes a layer from the UI stack.
func (h *Handle[RS]) PopLayer(layer Layer[RS]) {
	h.send(taskPopLayerMsg[RS]{layer: layer})
}

func (h *Handle[RS]) send(msg taskMsg) error {
	select {
	case h.outbox <- msg:
		return nil
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}

func (h *Handle[RS]) recv() (any, error) {
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

// IsTaskMsg reports whether msg is an internal task message that should
// be routed to TaskRunner.HandleMsg.
func IsTaskMsg(msg tea.Msg) bool {
	_, ok := msg.(taskMsg)
	return ok
}

type taskWaitForMsg struct {
	taskID uint64
	filter func(any) (deliver, handled bool)
}

type taskPushLayerMsg[RS any] struct {
	layer Layer[RS]
}

type taskPopLayerMsg[RS any] struct {
	layer Layer[RS]
}

type taskSendMsg struct {
	taskID  uint64
	payload any
}

type taskDoneMsg struct {
	taskID uint64
}

type taskSubscribeMsg struct {
	taskID uint64
	sub    *subscription
}

type taskUnsubscribeMsg struct {
	taskID uint64
	sub    *subscription
}

func (taskWaitForMsg) isTaskMsg()       {}
func (taskSendMsg) isTaskMsg()          {}
func (taskPushLayerMsg[RS]) isTaskMsg() {}
func (taskPopLayerMsg[RS]) isTaskMsg()  {}
func (taskDoneMsg) isTaskMsg()          {}
func (taskSubscribeMsg) isTaskMsg()     {}
func (taskUnsubscribeMsg) isTaskMsg()   {}

// subscription is a persistent filter that delivers matching messages
// to a channel. Used by Handle.Subscribe for streaming updates where
// the single-shot WaitFor would race between matches.
type subscription struct {
	filter func(any) bool
	ch     chan any
}

// TaskSendMsg is delivered to the app when a task calls Handle.Send().
// The app processes Payload on the bubbletea goroutine (safe for shared
// state access) and calls TaskRunner.Deliver(TaskID, response) when done.
type TaskSendMsg struct {
	TaskID  uint64
	Payload any
}

// taskState tracks a running task's WaitFor filter and any persistent
// Subscribe streams.
type taskState[RS any] struct {
	handle *Handle[RS]
	filter func(any) (deliver, handled bool)
	subs   []*subscription
}

// TaskRunner manages running tasks and bridges them to bubbletea.
type TaskRunner[RS any] struct {
	fromTasks chan taskMsg
	nextID    uint64
	mu        sync.Mutex // protects tasks map
	tasks     map[uint64]*taskState[RS]
}

// NewTaskRunner creates a TaskRunner.
func NewTaskRunner[RS any]() *TaskRunner[RS] {
	return &TaskRunner[RS]{
		fromTasks: make(chan taskMsg),
		tasks:     make(map[uint64]*taskState[RS]),
	}
}

// Run spawns a task goroutine. The function fn receives a Handle for
// synchronous communication with the bubbletea event loop.
func (r *TaskRunner[RS]) Run(fn func(*Handle[RS])) uint64 {
	r.nextID++
	id := r.nextID

	ctx, cancel := context.WithCancel(context.Background())
	h := &Handle[RS]{
		ctx:    ctx,
		cancel: cancel,
		outbox: r.fromTasks,
		inbox:  make(chan any, 1),
		id:     id,
	}

	r.mu.Lock()
	r.tasks[id] = &taskState[RS]{handle: h}
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
func (r *TaskRunner[RS]) Cancel(id uint64) {
	r.mu.Lock()
	ts, ok := r.tasks[id]
	r.mu.Unlock()
	if ok {
		ts.handle.cancel()
	}
}

// ListenCmd returns a tea.Cmd that blocks on the task channel.
// The app should call this from Init and after each task message delivery.
func (r *TaskRunner[RS]) ListenCmd() tea.Cmd {
	ch := r.fromTasks
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// CheckFilters runs active WaitFor filters and Subscribe subscriptions
// against msg. WaitFor filters are single-shot: a matching filter is
// cleared after delivery. Subscriptions are persistent: matching
// messages are sent to the subscription's channel and the subscription
// stays active until Unsubscribe. Returns handled=true if any filter or
// subscription requested that the message be hidden from layers.
func (r *TaskRunner[RS]) CheckFilters(msg any) (handled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, ts := range r.tasks {
		// Persistent subscriptions always see every message.
		for _, sub := range ts.subs {
			if !sub.filter(msg) {
				continue
			}
			select {
			case sub.ch <- msg:
			default:
				slog.Debug("task subscription channel full, dropping msg", "task", ts.handle.id)
			}
		}
		// Single-shot WaitFor filter.
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

// Deliver sends a response to a task that is blocked in Handle.Send().
// Must be called on the bubbletea goroutine.
func (r *TaskRunner[RS]) Deliver(taskID uint64, payload any) {
	r.mu.Lock()
	ts, ok := r.tasks[taskID]
	r.mu.Unlock()
	if ok {
		select {
		case ts.handle.inbox <- payload:
		case <-ts.handle.ctx.Done():
		}
	}
}

// HandleMsg processes a task message from the outbox channel. Returns
// a tea.Cmd for the app to execute (e.g. push/pop layer), or nil.
// The app should call ListenCmd again after handling each message.
func (r *TaskRunner[RS]) HandleMsg(msg tea.Msg) tea.Cmd {
	tmsg, ok := msg.(taskMsg)
	if !ok {
		return nil
	}

	switch msg := tmsg.(type) {
	case taskWaitForMsg:
		r.mu.Lock()
		if ts, ok := r.tasks[msg.taskID]; ok {
			ts.filter = msg.filter
		}
		r.mu.Unlock()
		return nil

	case taskSendMsg:
		return func() tea.Msg {
			return TaskSendMsg{TaskID: msg.taskID, Payload: msg.payload}
		}

	case taskPushLayerMsg[RS]:
		return func() tea.Msg { return PushLayerMsg[RS]{Layer: msg.layer} }

	case taskPopLayerMsg[RS]:
		layer := msg.layer
		return func() tea.Msg { return popLayerMsg[RS]{layer: layer} }

	case taskDoneMsg:
		r.mu.Lock()
		if ts, ok := r.tasks[msg.taskID]; ok {
			for _, sub := range ts.subs {
				close(sub.ch)
			}
		}
		delete(r.tasks, msg.taskID)
		r.mu.Unlock()
		return nil

	case taskSubscribeMsg:
		r.mu.Lock()
		if ts, ok := r.tasks[msg.taskID]; ok {
			ts.subs = append(ts.subs, msg.sub)
		}
		r.mu.Unlock()
		return nil

	case taskUnsubscribeMsg:
		r.mu.Lock()
		if ts, ok := r.tasks[msg.taskID]; ok {
			for i, sub := range ts.subs {
				if sub == msg.sub {
					ts.subs = append(ts.subs[:i], ts.subs[i+1:]...)
					close(sub.ch)
					break
				}
			}
		}
		r.mu.Unlock()
		return nil
	}
	return nil
}

// DriveOne reads one message from the outbox and processes it.
// For testing only — simulates what the app's Update does.
func (r *TaskRunner[RS]) DriveOne() tea.Msg {
	msg := <-r.fromTasks
	r.HandleMsg(msg)
	return msg
}
