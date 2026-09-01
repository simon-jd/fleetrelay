package relay

import (
	"errors"
	fleetrelayv1 "github.com/fleetrelay/fleetrelay/gen/fleetrelay/v1"
	"sync"
)

var (
	ErrNoConnection = errors.New("device has no local connection")
	ErrGeneration   = errors.New("target session generation is stale")
)

type RoutedCommand struct {
	Command        *fleetrelayv1.Command
	RedisMessageID string
}
type DeviceConnection struct {
	Generation uint64
	Commands   chan RoutedCommand
	Replaced   chan struct{}
}
type Registry struct {
	mu      sync.RWMutex
	devices map[string]*DeviceConnection
}

func NewRegistry() *Registry { return &Registry{devices: make(map[string]*DeviceConnection)} }
func (r *Registry) Register(id string, c *DeviceConnection) (old *DeviceConnection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.Replaced == nil {
		c.Replaced = make(chan struct{})
	}
	old = r.devices[id]
	if old != nil {
		close(old.Replaced)
	}
	r.devices[id] = c
	return old
}
func (r *Registry) Remove(id string, generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.devices[id]; c != nil && c.Generation == generation {
		delete(r.devices, id)
	}
}
func (r *Registry) Route(id string, generation uint64, c RoutedCommand) error {
	r.mu.RLock()
	conn := r.devices[id]
	r.mu.RUnlock()
	if conn == nil {
		return ErrNoConnection
	}
	if conn.Generation != generation {
		return ErrGeneration
	}
	select {
	case conn.Commands <- c:
		return nil
	default:
		return ErrSlowConsumer
	}
}
