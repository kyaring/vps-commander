package security

import (
	"errors"
	"sync"
	"time"
)

const (
	RiskLow    = 1
	RiskMedium = 2
	RiskHigh   = 3
)

type DevicePolicy struct {
	Mode string
}

type PolicySnapshot struct {
	Version    uint64
	GlobalRisk int
	Devices    map[string]DevicePolicy
	CreatedAt  time.Time
}

type Snapshot struct {
	mu   sync.RWMutex
	data PolicySnapshot
}

func New(globalRisk int, devices map[string]DevicePolicy, version uint64) *Snapshot {
	if globalRisk < RiskLow || globalRisk > RiskHigh {
		globalRisk = RiskMedium
	}
	cp := make(map[string]DevicePolicy, len(devices))
	for k, v := range devices {
		cp[k] = v
	}
	return &Snapshot{data: PolicySnapshot{
		Version: version, GlobalRisk: globalRisk, Devices: cp, CreatedAt: time.Now(),
	}}
}

func (s *Snapshot) Current() PolicySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make(map[string]DevicePolicy, len(s.data.Devices))
	for k, v := range s.data.Devices {
		cp[k] = v
	}
	snap := s.data
	snap.Devices = cp
	return snap
}

func (s *Snapshot) Publish(globalRisk int, devices map[string]DevicePolicy, version uint64) error {
	if globalRisk < RiskLow || globalRisk > RiskHigh {
		return errors.New("invalid global risk")
	}
	cp := make(map[string]DevicePolicy, len(devices))
	for k, v := range devices {
		cp[k] = v
	}
	s.mu.Lock()
	if version < s.data.Version {
		s.mu.Unlock()
		return errors.New("policy version regression")
	}
	s.data = PolicySnapshot{
		Version: version, GlobalRisk: globalRisk, Devices: cp, CreatedAt: time.Now(),
	}
	s.mu.Unlock()
	return nil
}

func (s *Snapshot) EffectiveRisk(device string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.data.Devices[device]; ok {
		if risk := ModeRisk(p.Mode); risk != 0 {
			return risk
		}
	}
	return s.data.GlobalRisk
}

func ModeRisk(mode string) int {
	switch mode {
	case "low":
		return RiskLow
	case "medium":
		return RiskMedium
	case "high":
		return RiskHigh
	default:
		return 0
	}
}
