package upstream

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"influx-bouncer/internal/config"
	"influx-bouncer/internal/metrics"
)

type targetState struct {
	consecutiveFailures int
	circuitOpenUntil    time.Time
}

type Manager struct {
	cfg          config.Config
	client       *http.Client
	metrics      *metrics.Metrics
	rr           uint64
	targetStates map[string]*targetState
	mu           sync.RWMutex
}

func NewManager(cfg config.Config, client *http.Client, m *metrics.Metrics) *Manager {
	mgr := &Manager{
		cfg:     cfg,
		client:  client,
		metrics: m,
		targetStates: func() map[string]*targetState {
			out := make(map[string]*targetState, len(cfg.Targets))
			for _, t := range cfg.Targets {
				out[t.Name] = &targetState{}
			}
			return out
		}(),
	}
	if cfg.ProbeInterval > 0 {
		go mgr.healthLoop()
	}
	return mgr
}

func (m *Manager) PickTarget(exclude map[string]struct{}) (config.Target, bool) {
	now := time.Now()
	weighted := make([]config.Target, 0, len(m.cfg.Targets)*2)
	fallback := make([]config.Target, 0, len(m.cfg.Targets)*2)

	m.mu.RLock()
	for _, t := range m.cfg.Targets {
		if _, skip := exclude[t.Name]; skip {
			continue
		}
		state := m.targetStates[t.Name]
		isOpen := state != nil && now.Before(state.circuitOpenUntil)
		for i := 0; i < t.Weight; i++ {
			fallback = append(fallback, t)
			if !isOpen {
				weighted = append(weighted, t)
			}
		}
	}
	m.mu.RUnlock()

	candidates := weighted
	if len(candidates) == 0 {
		candidates = fallback
	}
	if len(candidates) == 0 {
		return config.Target{}, false
	}
	idx := atomic.AddUint64(&m.rr, 1)
	return candidates[idx%uint64(len(candidates))], true
}

func (m *Manager) RecordSuccess(targetName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.ensureStateLocked(targetName)
	state.consecutiveFailures = 0
	state.circuitOpenUntil = time.Time{}
}

func (m *Manager) RecordFailure(targetName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.ensureStateLocked(targetName)
	state.consecutiveFailures++
	if state.consecutiveFailures >= m.cfg.BreakerFailures {
		state.circuitOpenUntil = time.Now().Add(m.cfg.BreakerCooldown)
		state.consecutiveFailures = 0
		slog.Info("target circuit opened", "target", targetName, "duration", m.cfg.BreakerCooldown)
	}
}

func (m *Manager) ensureStateLocked(targetName string) *targetState {
	state, ok := m.targetStates[targetName]
	if !ok {
		state = &targetState{}
		m.targetStates[targetName] = state
	}
	return state
}

func (m *Manager) healthLoop() {
	ticker := time.NewTicker(m.cfg.ProbeInterval)
	defer ticker.Stop()

	for range ticker.C {
		for _, target := range m.cfg.Targets {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			healthURL, err := url.JoinPath(target.URL, "/health")
			if err != nil {
				cancel()
				m.RecordFailure(target.Name)
				continue
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
			if err != nil {
				cancel()
				m.RecordFailure(target.Name)
				continue
			}
			resp, err := m.client.Do(req)
			cancel()
			if err != nil {
				m.RecordFailure(target.Name)
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				m.RecordSuccess(target.Name)
			} else {
				m.RecordFailure(target.Name)
			}
		}
	}
}
