package tasks

import (
	"context"
	"fmt"
	"sync"
)

// Memory is an in-process Store for tests and `tavern-core -store memory`.
type Memory struct {
	mu     sync.Mutex
	tasks  map[string]*Task
	seq    int
	Events []Event
}

func NewMemory() *Memory { return &Memory{tasks: map[string]*Task{}} }

func (m *Memory) Get(_ context.Context, id string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := *t
	return &c, nil
}

func (m *Memory) Put(_ context.Context, t *Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *t
	m.tasks[t.ID] = &c
	return nil
}

func (m *Memory) List(_ context.Context) ([]*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		c := *t
		out = append(out, &c)
	}
	return out, nil
}

func (m *Memory) NextID(_ context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	return fmt.Sprintf("T-%d", m.seq), nil
}

func (m *Memory) Emit(_ context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Events = append(m.Events, e)
	return nil
}
