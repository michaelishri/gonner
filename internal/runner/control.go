package runner

import (
	"errors"
	"time"
)

var ErrGeneration = errors.New("generation conflict")

// InstanceInfo describes an individual launch, never an aggregate or caller PID.
type InstanceInfo struct {
	ID                 string       `json:"id"`
	Generation         string       `json:"generation"`
	State              ProcessState `json:"state"`
	Controllable       bool         `json:"controllable"`
	PreviousGeneration string       `json:"previousGeneration"`
	PreviousReaped     bool         `json:"previousReaped"`
}

func (p *Process) instanceInfo() InstanceInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return InstanceInfo{p.Name(), p.generation, p.State(), p.cfg.Controllable, p.previousGeneration, p.previousReaped}
}

func (p *Process) requestedRestart() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restartRequested
}

// restart atomically claims the current generation. Duplicate requests are
// idempotent; the request channel belongs only to this launch's execution.
func (p *Process) restart(expected string, grace time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.cfg.Controllable || expected == "" || expected != p.generation {
		return ErrGeneration
	}
	if p.restartRequested {
		return nil
	}
	if p.State() != StateRunning || p.pid == 0 || p.generationStop == nil {
		return ErrGeneration
	}
	p.restartRequested = true
	p.state.Store(StateStopping)
	p.generationStop <- grace
	return nil
}

func (m *Manager) Instances() []InstanceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := []InstanceInfo{}
	for _, p := range m.processes {
		result = append(result, p.instanceInfo())
	}
	return result
}

func (m *Manager) Restart(id, expected string, grace time.Duration) error {
	if m.IsShuttingDown() {
		return ErrGeneration
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, p := range m.processes {
		if p.Name() == id {
			return p.restart(expected, grace)
		}
	}
	return ErrGeneration
}
