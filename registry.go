package cbreak

import (
	"reflect"
	"sync"
)

// Registry manages a collection of circuit breakers, providing a way to
// create, retrieve, and manage multiple circuit breaker instances.
// It is safe for concurrent use.
type Registry[T any] struct {
	mu       sync.RWMutex
	breakers map[string]*Breaker[T]
}

// NewRegistry creates a new empty registry for managing circuit breakers.
// The registry is safe for concurrent use.
func NewRegistry[T any]() *Registry[T] {
	return &Registry[T]{
		breakers: make(map[string]*Breaker[T]),
	}
}

// GetOrCreate retrieves an existing circuit breaker by name or creates a new one
// if it doesn't exist. If a new breaker is created, it uses the provided config.
// If config is nil, DefaultConfig is used.
func (r *Registry[T]) GetOrCreate(name string, config *Config) (*Breaker[T], error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if b, ok := r.breakers[name]; ok {
		return b, nil
	}

	if config == nil {
		config = DefaultConfig(name)
	}

	b, err := NewBreaker[T](config)
	if err != nil {
		return nil, err
	}

	r.breakers[name] = b

	return b, nil
}

// Remove deletes a circuit breaker from the registry by name.
// If the breaker doesn't exist, this is a no-op.
func (r *Registry[T]) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.breakers, name)
}

// ResetAll resets all circuit breakers in the registry to their initial state.
// This is useful for testing or when you want to clear the state of all breakers.
func (r *Registry[T]) ResetAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, b := range r.breakers {
		b.Reset()
	}
}

// GetStates returns a map of all circuit breaker names to their current states.
// This is useful for monitoring the state of all breakers in the registry.
func (r *Registry[T]) GetStates() map[string]State {
	r.mu.RLock()
	defer r.mu.RUnlock()

	states := make(map[string]State)
	for name, b := range r.breakers {
		states[name] = b.GetState()
	}

	return states
}

// GetMetrics returns a map of all circuit breaker names to their current metrics.
// This is useful for monitoring the performance and health of all breakers.
func (r *Registry[T]) GetMetrics() map[string]*Metrics {
	r.mu.RLock()
	defer r.mu.RUnlock()

	metrics := make(map[string]*Metrics)
	for name, breaker := range r.breakers {
		metrics[name] = breaker.GetMetrics()
	}

	return metrics
}

// DefaultRegistry returns the global registry instance for the given type.
// This registry is shared across all code that uses the same type parameter.
func DefaultRegistry[T any]() *Registry[T] {
	return getDefaultRegistry[T]()
}

var defaultRegistry = sync.Map{}

func getDefaultRegistry[T any]() *Registry[T] {
	// Use a type-specific key to ensure uniqueness
	key := reflect.TypeOf((*T)(nil)).Elem()
	value, _ := defaultRegistry.LoadOrStore(key, NewRegistry[T]())

	return value.(*Registry[T])
}

// Get retrieves or creates a circuit breaker from the default registry.
// If the breaker doesn't exist, it is created with DefaultConfig.
func Get[T any](name string) (*Breaker[T], error) {
	return DefaultRegistry[T]().GetOrCreate(name, DefaultConfig(name))
}

// GetWithConfig retrieves or creates a circuit breaker from the default registry
// with the specified configuration. Returns ErrInvalidConfig if config is nil.
func GetWithConfig[T any](name string, config *Config) (*Breaker[T], error) {
	if config == nil {
		return nil, ErrInvalidConfig
	}

	return DefaultRegistry[T]().GetOrCreate(name, config)
}

// Remove deletes a circuit breaker from the default registry.
// If the breaker doesn't exist, this is a no-op.
func Remove[T any](name string) {
	DefaultRegistry[T]().Remove(name)
}

// ResetAll resets all circuit breakers in the default registry to their initial state.
func ResetAll[T any]() {
	DefaultRegistry[T]().ResetAll()
}

// GetStates returns a map of all circuit breaker names to their current states
// from the default registry.
func GetStates[T any]() map[string]State {
	return DefaultRegistry[T]().GetStates()
}

// GetMetrics returns a map of all circuit breaker names to their current metrics
// from the default registry.
func GetMetrics[T any]() map[string]*Metrics {
	return DefaultRegistry[T]().GetMetrics()
}
