// Package cbreak provides a flexible and configurable circuit breaker implementation
// for Go applications. It helps prevent cascading failures in distributed systems
// by monitoring for failures and stopping the flow of requests when necessary.
//
// The package offers:
//   - Generic circuit breaker implementation with type-safe execution
//   - Configurable failure thresholds and timeouts
//   - Metrics collection and monitoring
//   - State change notifications
//   - Global registry for managing multiple circuit breakers
//
// Example usage:
//
//	breaker, err := cbreak.NewBreaker[string](cbreak.DefaultConfig("my-service"))
//	if err != nil {
//	    log.Fatal(err)
//	}
//	result, err := breaker.Execute(ctx, func() (string, error) {
//	    return "success", nil
//	})
package cbreak

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Core error types
var (
	ErrCircuitOpen   = errors.New("circuit breaker is open")
	ErrTimeout       = errors.New("operation timed out")
	ErrNilFunction   = errors.New("nil function")
	ErrInvalidConfig = errors.New("invalid configuration")
)

// State represents the state of a circuit breaker
type State int

const (
	// Closed state allows operations to proceed normally
	Closed State = iota + 1
	// HalfOpen state allows a limited number of test operations
	HalfOpen
	// Open state prevents operations from executing
	Open
)

// String returns the string representation of a state
func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// StateChangeEvent represents a circuit breaker state transition
type StateChangeEvent struct {
	From      State
	To        State
	Timestamp time.Time
	Reason    string
}

// Metrics holds basic circuit breaker metrics
type Metrics struct {
	TotalRequests   int64
	SuccessfulCalls int64
	FailedCalls     int64
	RejectedCalls   int64
	TimeoutCalls    int64
	FailureRate     float64
	State           State
	LastError       error
	LastErrorTime   time.Time
}

// Config holds the essential configuration for a circuit breaker
type Config struct {
	// Name identifies the circuit breaker instance
	Name string

	// Thresholds control when the circuit breaker opens
	// FailureThreshold is the number of consecutive failures before opening
	FailureThreshold int
	// SuccessThreshold is the number of consecutive successes required to close
	SuccessThreshold int
	// FailureRateThreshold is the percentage of failures that triggers opening (0-100)
	FailureRateThreshold float64

	// Timeouts control operation timing
	// Timeout is the duration before the circuit breaker resets from open to half-open
	Timeout time.Duration
	// CommandTimeout is the maximum duration for each operation
	CommandTimeout time.Duration

	// HalfOpenMaxRequests limits concurrent requests during half-open state
	HalfOpenMaxRequests int

	// OnStateChange is called when the circuit breaker changes state
	OnStateChange func(from, to State, reason string)

	// ErrorClassifier determines if an error should be counted as a failure
	ErrorClassifier func(error) bool
}

// Result represents the outcome of an operation executed through the circuit breaker.
// It contains both the operation's return value and any error that occurred.
type Result[T any] struct {
	// Value is the successful result of the operation
	Value T
	// Err is any error that occurred during execution
	Err error
}

// DefaultConfig returns a sensible default circuit breaker configuration
func DefaultConfig(name string) *Config {
	return &Config{
		Name:                 name,
		FailureThreshold:     5,
		SuccessThreshold:     3,
		FailureRateThreshold: 50.0,
		Timeout:              30 * time.Second,
		CommandTimeout:       10 * time.Second,
		HalfOpenMaxRequests:  5,
		ErrorClassifier:      func(err error) bool { return err != nil },
	}
}

// Breaker is a simplified circuit breaker implementation
type Breaker[T any] struct {
	mu      sync.RWMutex
	state   State
	config  *Config
	metrics *Metrics

	failures         int64
	successes        int64
	lastStateChange  time.Time
	halfOpenRequests int32

	events       []StateChangeEvent
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

// NewBreaker creates a new circuit breaker with the given configuration
func NewBreaker[T any](config *Config) (*Breaker[T], error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	if config == nil {
		config = DefaultConfig("default")
	}

	now := time.Now()
	breaker := &Breaker[T]{
		config:          config,
		state:           Closed,
		failures:        0,
		successes:       0,
		lastStateChange: now,
		events:          make([]StateChangeEvent, 0, 10),
		metrics: &Metrics{
			State:         Closed,
			LastErrorTime: now,
		},
		shutdown: make(chan struct{}),
	}

	return breaker, nil
}

// Execute executes the given function with circuit breaker protection
func (b *Breaker[T]) Execute(ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T

	// Check for nil function
	if fn == nil {
		atomic.AddInt64(&b.metrics.RejectedCalls, 1)
		return zero, ErrNilFunction
	}

	// Check if circuit breaker is shutting down
	select {
	case <-b.shutdown:
		atomic.AddInt64(&b.metrics.RejectedCalls, 1)
		return zero, ErrCircuitOpen
	default:
	}

	// Increment total requests counter
	atomic.AddInt64(&b.metrics.TotalRequests, 1)

	// Get current state and handle accordingly
	state := b.GetState()
	switch state {
	case Open:
		atomic.AddInt64(&b.metrics.RejectedCalls, 1)
		return zero, ErrCircuitOpen
	case HalfOpen:
		// Try to increment half-open requests counter
		currentRequests := atomic.AddInt32(&b.halfOpenRequests, 1)
		if currentRequests > int32(b.config.HalfOpenMaxRequests) {
			// Too many requests, decrement counter and reject
			atomic.AddInt32(&b.halfOpenRequests, -1)
			atomic.AddInt64(&b.metrics.RejectedCalls, 1)
			return zero, ErrCircuitOpen
		}
		// Ensure we always decrement the counter after execution
		defer atomic.AddInt32(&b.halfOpenRequests, -1)
	}

	// Execute with timeout
	resultCh := make(chan Result[T], 1)

	// Add a timeout if not already present in the context
	execCtx := ctx
	if _, ok := ctx.Deadline(); !ok && b.config.CommandTimeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, b.config.CommandTimeout)
		defer cancel()
	}

	// Execute in goroutine to handle panics and timeouts
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultCh <- Result[T]{
					Err: fmt.Errorf("panic in execution: %v", r),
				}
			}
		}()

		result, err := fn()
		resultCh <- Result[T]{
			Value: result,
			Err:   err,
		}
	}()

	// Wait for result or timeout
	select {
	case <-execCtx.Done():
		atomic.AddInt64(&b.metrics.TimeoutCalls, 1)
		err := execCtx.Err()
		if err == context.Canceled {
			// Context cancellation should not count as a failure
			atomic.AddInt64(&b.metrics.SuccessfulCalls, 1)
		} else {
			// Timeout should count as a failure
			atomic.AddInt64(&b.metrics.FailedCalls, 1)
		}
		return zero, err
	case res := <-resultCh:
		// Handle result and update metrics
		b.handleResult(res.Err)
		return res.Value, res.Err
	}
}

// handleResult processes the result of an operation and updates metrics
func (b *Breaker[T]) handleResult(err error) {
	if err != nil {
		b.handleError(err)
	} else {
		b.handleSuccess()
	}
}

func (b *Breaker[T]) handleError(err error) {
	// Check if error should be counted as failure
	if b.config.ErrorClassifier != nil && !b.config.ErrorClassifier(err) {
		atomic.AddInt64(&b.metrics.SuccessfulCalls, 1)
		return
	}

	// Record failure atomically
	atomic.AddInt64(&b.metrics.FailedCalls, 1)
	atomic.AddInt64(&b.failures, 1)

	b.mu.Lock()
	b.metrics.LastError = err
	b.metrics.LastErrorTime = time.Now()

	// Calculate failure rate with atomic loads to ensure consistency
	successfulCalls := atomic.LoadInt64(&b.metrics.SuccessfulCalls)
	failedCalls := atomic.LoadInt64(&b.metrics.FailedCalls)
	totalCalls := successfulCalls + failedCalls

	var failureRate float64
	if totalCalls > 0 {
		failureRate = float64(failedCalls) / float64(totalCalls) * 100.0
	}

	// Update metrics
	b.metrics.FailureRate = failureRate

	// Check if we need to transition to Open state
	currentState := b.state
	failures := atomic.LoadInt64(&b.failures)
	if b.shouldTransitionToOpen(failures, failureRate, totalCalls, currentState) {
		// Reset counters atomically before state transition
		atomic.StoreInt64(&b.failures, 0)
		atomic.StoreInt64(&b.successes, 0)
		atomic.StoreInt32(&b.halfOpenRequests, 0)

		// Update state and timestamp
		b.state = Open
		b.lastStateChange = time.Now()
		b.metrics.State = Open

		// Record state transition
		event := StateChangeEvent{
			From:      currentState,
			To:        Open,
			Timestamp: b.lastStateChange,
			Reason:    fmt.Sprintf("threshold exceeded (%d failures, %.2f%% failure rate)", failures, failureRate),
		}
		b.events = append(b.events, event)

		// Make callback if it exists
		if b.config.OnStateChange != nil {
			b.config.OnStateChange(currentState, Open, fmt.Sprintf("threshold exceeded (%d failures, %.2f%% failure rate)", failures, failureRate))
		}
	}
	b.mu.Unlock()
}

func (b *Breaker[T]) shouldTransitionToOpen(failures int64, failureRate float64, totalCalls int64, currentState State) bool {
	if currentState == HalfOpen {
		return true // Any failure in half-open state should transition to open
	}
	if failures >= int64(b.config.FailureThreshold) && currentState == Closed {
		return true
	}
	return failureRate >= b.config.FailureRateThreshold && totalCalls >= 5 && currentState == Closed
}

func (b *Breaker[T]) handleSuccess() {
	// Record success atomically
	atomic.AddInt64(&b.metrics.SuccessfulCalls, 1)

	// Calculate failure rate with atomic loads to ensure consistency
	successfulCalls := atomic.LoadInt64(&b.metrics.SuccessfulCalls)
	failedCalls := atomic.LoadInt64(&b.metrics.FailedCalls)
	totalCalls := successfulCalls + failedCalls

	var failureRate float64
	if totalCalls > 0 {
		failureRate = float64(failedCalls) / float64(totalCalls) * 100.0
	}

	// Update metrics atomically
	b.mu.Lock()
	b.metrics.FailureRate = failureRate
	currentState := b.state
	b.mu.Unlock()

	// Handle success in HalfOpen state
	if currentState == HalfOpen {
		successes := atomic.AddInt64(&b.successes, 1)
		if successes >= int64(b.config.SuccessThreshold) {
			b.mu.Lock()
			// Double check we're still in HalfOpen state
			if b.state == HalfOpen {
				// Reset counters atomically before state transition
				atomic.StoreInt64(&b.failures, 0)
				atomic.StoreInt64(&b.successes, 0)
				atomic.StoreInt32(&b.halfOpenRequests, 0)

				// Update state and timestamp
				b.state = Closed
				b.lastStateChange = time.Now()
				b.metrics.State = Closed

				// Record state transition
				event := StateChangeEvent{
					From:      HalfOpen,
					To:        Closed,
					Timestamp: b.lastStateChange,
					Reason:    "success threshold reached",
				}
				b.events = append(b.events, event)

				// Make callback if it exists
				if b.config.OnStateChange != nil {
					b.config.OnStateChange(HalfOpen, Closed, "success threshold reached")
				}
			}
			b.mu.Unlock()
		}
	}
}

// GetState returns the current state of the circuit breaker
func (b *Breaker[T]) GetState() State {
	// First try with a read lock
	b.mu.RLock()
	state := b.state
	lastChange := b.lastStateChange
	b.mu.RUnlock()

	// Check if we need to transition to HalfOpen
	if state == Open && time.Since(lastChange) >= b.config.Timeout {
		// Upgrade to write lock for state transition
		b.mu.Lock()
		// Double check the state hasn't changed
		if b.state == Open && time.Since(b.lastStateChange) >= b.config.Timeout {
			// Reset counters atomically before state transition
			atomic.StoreInt64(&b.failures, 0)
			atomic.StoreInt64(&b.successes, 0)
			atomic.StoreInt32(&b.halfOpenRequests, 0)

			// Update state and timestamp
			b.state = HalfOpen
			b.lastStateChange = time.Now()
			b.metrics.State = HalfOpen

			// Record state transition
			event := StateChangeEvent{
				From:      Open,
				To:        HalfOpen,
				Timestamp: b.lastStateChange,
				Reason:    "timeout elapsed",
			}
			b.events = append(b.events, event)

			// Make callback if it exists
			if b.config.OnStateChange != nil {
				b.config.OnStateChange(Open, HalfOpen, "timeout elapsed")
			}
			state = HalfOpen
		}
		b.mu.Unlock()
	}

	return state
}

// transitionState transitions the circuit breaker from one state to another
func (b *Breaker[T]) transitionState(from, to State, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Only transition if we're still in the 'from' state
	if b.state != from {
		return
	}

	// Update state and timestamp
	b.state = to
	b.lastStateChange = time.Now()
	b.metrics.State = to

	// Record state transition
	event := StateChangeEvent{
		From:      from,
		To:        to,
		Timestamp: b.lastStateChange,
		Reason:    reason,
	}
	b.events = append(b.events, event)

	// Reset counters based on state transition
	switch to {
	case HalfOpen:
		atomic.StoreInt64(&b.failures, 0)
		atomic.StoreInt64(&b.successes, 0)
		atomic.StoreInt32(&b.halfOpenRequests, 0)
	case Closed:
		atomic.StoreInt64(&b.failures, 0)
		atomic.StoreInt64(&b.successes, 0)
		atomic.StoreInt32(&b.halfOpenRequests, 0)
	}

	// Make callback if it exists
	if b.config.OnStateChange != nil {
		b.config.OnStateChange(from, to, reason)
	}
}

// SetState manually sets the circuit breaker state
func (b *Breaker[T]) SetState(state State, reason string) bool {
	b.mu.RLock()
	oldState := b.state
	b.mu.RUnlock()

	if oldState == state {
		return false
	}

	b.transitionState(oldState, state, reason)
	return true
}

// GetMetrics returns the current metrics of the circuit breaker
func (b *Breaker[T]) GetMetrics() *Metrics {
	// Create a copy of metrics to avoid race conditions
	currentMetrics := &Metrics{
		TotalRequests:   atomic.LoadInt64(&b.metrics.TotalRequests),
		SuccessfulCalls: atomic.LoadInt64(&b.metrics.SuccessfulCalls),
		FailedCalls:     atomic.LoadInt64(&b.metrics.FailedCalls),
		RejectedCalls:   atomic.LoadInt64(&b.metrics.RejectedCalls),
		TimeoutCalls:    atomic.LoadInt64(&b.metrics.TimeoutCalls),
		FailureRate:     b.metrics.FailureRate,
		State:           b.GetState(),
		LastError:       b.metrics.LastError,
		LastErrorTime:   b.metrics.LastErrorTime,
	}

	return currentMetrics
}

// Reset resets the circuit breaker to its initial state
func (b *Breaker[T]) Reset() {
	b.mu.Lock()
	b.state = Closed
	b.lastStateChange = time.Now()
	b.mu.Unlock()

	// Reset counters atomically
	atomic.StoreInt64(&b.failures, 0)
	atomic.StoreInt64(&b.successes, 0)
	atomic.StoreInt32(&b.halfOpenRequests, 0)

	// Reset metrics
	b.metrics = &Metrics{State: Closed}
}

// Shutdown cleanly shuts down the circuit breaker
func (b *Breaker[T]) Shutdown() {
	b.shutdownOnce.Do(func() {
		close(b.shutdown)
	})
}

// validateConfig validates the circuit breaker configuration
func validateConfig(config *Config) error {
	if config == nil {
		return ErrInvalidConfig
	}

	if config.Name == "" {
		return errors.New("invalid circuit breaker name")
	}

	if config.FailureThreshold <= 0 {
		return errors.New("invalid failure threshold")
	}

	if config.SuccessThreshold <= 0 {
		return errors.New("invalid success threshold")
	}

	if config.FailureRateThreshold < 0 || config.FailureRateThreshold > 100 {
		return errors.New("invalid failure rate threshold")
	}

	if config.Timeout <= 0 {
		return errors.New("invalid timeout")
	}

	if config.CommandTimeout <= 0 {
		return errors.New("invalid command timeout")
	}

	if config.HalfOpenMaxRequests <= 0 {
		return errors.New("invalid half-open max requests")
	}

	return nil
}

// ExecuteWithFallback runs the function with a fallback if it fails
func (b *Breaker[T]) ExecuteWithFallback(ctx context.Context, fn func() (T, error), fallback func(error) (T, error)) (T, error) {
	result, err := b.Execute(ctx, fn)
	if err != nil {
		return fallback(err)
	}
	return result, nil
}
