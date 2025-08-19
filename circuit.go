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

// Breaker is a performance-optimized circuit breaker implementation
type Breaker[T any] struct {
	// Atomic state for lock-free reads
	state atomic.Value // stores State

	// Configuration (immutable after creation)
	config *Config

	// Metrics with atomic operations
	metrics struct {
		TotalRequests   int64
		SuccessfulCalls int64
		FailedCalls     int64
		RejectedCalls   int64
		TimeoutCalls    int64
		FailureRate     int64 // atomic storage for failure rate (stored as percentage * 100)
		LastError       error
		LastErrorTime   int64 // atomic timestamp
	}

	// Counters with atomic operations
	failures         int64
	successes        int64
	halfOpenRequests int32

	// State transition tracking (protected by mu)
	mu              sync.RWMutex
	lastStateChange int64 // atomic timestamp
	events          []StateChangeEvent

	// Shutdown control
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

// NewBreaker creates a new circuit breaker with the given configuration
func NewBreaker[T any](config *Config) (*Breaker[T], error) {
	err := validateConfig(config)
	if err != nil {
		return nil, err
	}

	if config == nil {
		config = DefaultConfig("default")
	}

	now := time.Now().UnixNano()
	breaker := &Breaker[T]{
		config:   config,
		events:   make([]StateChangeEvent, 0, 10),
		shutdown: make(chan struct{}),
	}

	// Initialize atomic state
	breaker.state.Store(Closed)
	breaker.lastStateChange = now
	breaker.metrics.LastErrorTime = now

	return breaker, nil
}

// GetState returns the current state of the circuit breaker (lock-free)
func (b *Breaker[T]) GetState() State {
	if state, ok := b.state.Load().(State); ok {
		// Check if we need to transition from Open to HalfOpen
		if state == Open {
			b.checkAndTransitionToHalfOpen()
			// Re-read state after potential transition
			if newState, ok := b.state.Load().(State); ok {
				return newState
			}
		}

		return state
	}

	return Closed
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

	// Get current state and handle accordingly (lock-free)
	state := b.GetState()

	// Check if we need to transition from Open to HalfOpen
	if state == Open {
		b.checkAndTransitionToHalfOpen()
		state = b.GetState() // Re-read state after potential transition
	}

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

	// Update error info atomically
	atomic.StoreInt64(&b.metrics.LastErrorTime, time.Now().UnixNano())

	// Protect LastError field with mutex to avoid race conditions
	b.mu.Lock()
	b.metrics.LastError = err
	b.mu.Unlock()

	// Calculate failure rate with atomic loads to ensure consistency
	successfulCalls := atomic.LoadInt64(&b.metrics.SuccessfulCalls)
	failedCalls := atomic.LoadInt64(&b.metrics.FailedCalls)
	totalCalls := successfulCalls + failedCalls

	var failureRate int64
	if totalCalls > 0 {
		failureRate = int64(float64(failedCalls) / float64(totalCalls) * 100.0)
	}

	// Update metrics atomically
	atomic.StoreInt64(&b.metrics.FailureRate, failureRate)

	// Check if we need to transition to Open state
	currentState := b.GetState()

	failures := atomic.LoadInt64(&b.failures)
	if b.shouldTransitionToOpen(failures, float64(failureRate), totalCalls, currentState) {
		b.transitionToOpen(currentState, failures, float64(failureRate))
	}
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

func (b *Breaker[T]) transitionToOpen(fromState State, failures int64, failureRate float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Double-check state hasn't changed
	currentState := b.GetState()
	if currentState != fromState {
		return
	}

	// Reset counters atomically before state transition
	atomic.StoreInt64(&b.failures, 0)
	atomic.StoreInt64(&b.successes, 0)
	atomic.StoreInt32(&b.halfOpenRequests, 0)

	// Update state and timestamp
	now := time.Now()

	b.state.Store(Open)
	atomic.StoreInt64(&b.lastStateChange, now.UnixNano())

	// Record state transition
	event := StateChangeEvent{
		From:      fromState,
		To:        Open,
		Timestamp: now,
		Reason:    fmt.Sprintf("threshold exceeded (%d failures, %.2f%% failure rate)", failures, failureRate),
	}
	b.events = append(b.events, event)

	// Make callback if it exists
	if b.config.OnStateChange != nil {
		b.config.OnStateChange(fromState, Open, fmt.Sprintf("threshold exceeded (%d failures, %.2f%% failure rate)", failures, failureRate))
	}
}

func (b *Breaker[T]) handleSuccess() {
	// Record success atomically
	atomic.AddInt64(&b.metrics.SuccessfulCalls, 1)

	// Calculate failure rate with atomic loads to ensure consistency
	successfulCalls := atomic.LoadInt64(&b.metrics.SuccessfulCalls)
	failedCalls := atomic.LoadInt64(&b.metrics.FailedCalls)
	totalCalls := successfulCalls + failedCalls

	var failureRate int64
	if totalCalls > 0 {
		failureRate = int64(float64(failedCalls) / float64(totalCalls) * 100.0)
	}

	// Update metrics atomically
	atomic.StoreInt64(&b.metrics.FailureRate, failureRate)
	currentState := b.GetState()

	// Handle success in HalfOpen state
	if currentState == HalfOpen {
		successes := atomic.AddInt64(&b.successes, 1)
		if successes >= int64(b.config.SuccessThreshold) {
			b.transitionToClosed()
		}
	}
}

func (b *Breaker[T]) transitionToClosed() {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Double check we're still in HalfOpen state
	if b.GetState() != HalfOpen {
		return
	}

	// Reset counters atomically before state transition
	atomic.StoreInt64(&b.failures, 0)
	atomic.StoreInt64(&b.successes, 0)
	atomic.StoreInt32(&b.halfOpenRequests, 0)

	// Update state and timestamp
	now := time.Now()

	b.state.Store(Closed)
	atomic.StoreInt64(&b.lastStateChange, now.UnixNano())

	// Record state transition
	event := StateChangeEvent{
		From:      HalfOpen,
		To:        Closed,
		Timestamp: now,
		Reason:    "success threshold reached",
	}
	b.events = append(b.events, event)

	// Make callback if it exists
	if b.config.OnStateChange != nil {
		b.config.OnStateChange(HalfOpen, Closed, "success threshold reached")
	}
}

// checkAndTransitionToHalfOpen checks if we need to transition from Open to HalfOpen
func (b *Breaker[T]) checkAndTransitionToHalfOpen() {
	lastChange := atomic.LoadInt64(&b.lastStateChange)
	timeoutNanos := b.config.Timeout.Nanoseconds()
	currentTime := time.Now().UnixNano()

	if currentTime-lastChange >= timeoutNanos {
		b.mu.Lock()
		defer b.mu.Unlock()

		// Double check the state hasn't changed and timeout still applies
		if currentState, ok := b.state.Load().(State); ok && currentState == Open {
			if currentTime-atomic.LoadInt64(&b.lastStateChange) >= timeoutNanos {
				// Reset counters atomically before state transition
				atomic.StoreInt64(&b.failures, 0)
				atomic.StoreInt64(&b.successes, 0)
				atomic.StoreInt32(&b.halfOpenRequests, 0)

				// Update state and timestamp
				now := time.Now()

				b.state.Store(HalfOpen)
				atomic.StoreInt64(&b.lastStateChange, now.UnixNano())

				// Record state transition
				event := StateChangeEvent{
					From:      Open,
					To:        HalfOpen,
					Timestamp: now,
					Reason:    "timeout elapsed",
				}
				b.events = append(b.events, event)

				// Make callback if it exists
				if b.config.OnStateChange != nil {
					b.config.OnStateChange(Open, HalfOpen, "timeout elapsed")
				}
			}
		}
	}
}

// GetMetrics returns the current metrics of the circuit breaker
func (b *Breaker[T]) GetMetrics() *Metrics {
	// Create a copy of metrics to avoid race conditions
	lastErrorTime := atomic.LoadInt64(&b.metrics.LastErrorTime)

	// Protect LastError field access
	b.mu.RLock()
	lastError := b.metrics.LastError
	b.mu.RUnlock()

	currentMetrics := &Metrics{
		TotalRequests:   atomic.LoadInt64(&b.metrics.TotalRequests),
		SuccessfulCalls: atomic.LoadInt64(&b.metrics.SuccessfulCalls),
		FailedCalls:     atomic.LoadInt64(&b.metrics.FailedCalls),
		RejectedCalls:   atomic.LoadInt64(&b.metrics.RejectedCalls),
		TimeoutCalls:    atomic.LoadInt64(&b.metrics.TimeoutCalls),
		FailureRate:     float64(atomic.LoadInt64(&b.metrics.FailureRate)),
		State:           b.GetState(),
		LastError:       lastError,
		LastErrorTime:   time.Unix(0, lastErrorTime),
	}

	return currentMetrics
}

// Reset resets the circuit breaker to its initial state
func (b *Breaker[T]) Reset() {
	b.mu.Lock()

	now := time.Now()

	b.state.Store(Closed)
	atomic.StoreInt64(&b.lastStateChange, now.UnixNano())
	b.mu.Unlock()

	// Reset counters atomically
	atomic.StoreInt64(&b.failures, 0)
	atomic.StoreInt64(&b.successes, 0)
	atomic.StoreInt32(&b.halfOpenRequests, 0)

	// Reset metrics
	atomic.StoreInt64(&b.metrics.TotalRequests, 0)
	atomic.StoreInt64(&b.metrics.SuccessfulCalls, 0)
	atomic.StoreInt64(&b.metrics.FailedCalls, 0)
	atomic.StoreInt64(&b.metrics.RejectedCalls, 0)
	atomic.StoreInt64(&b.metrics.TimeoutCalls, 0)
	atomic.StoreInt64(&b.metrics.FailureRate, 0)

	// Protect LastError field reset
	b.mu.Lock()
	b.metrics.LastError = nil
	b.mu.Unlock()

	atomic.StoreInt64(&b.metrics.LastErrorTime, now.UnixNano())
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

// SetState manually sets the circuit breaker state
func (b *Breaker[T]) SetState(state State, reason string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	oldState := b.GetState()
	if oldState == state {
		return false
	}

	// Update state and timestamp
	now := time.Now()

	b.state.Store(state)
	atomic.StoreInt64(&b.lastStateChange, now.UnixNano())

	// Record state transition
	event := StateChangeEvent{
		From:      oldState,
		To:        state,
		Timestamp: now,
		Reason:    reason,
	}
	b.events = append(b.events, event)

	// Reset counters based on state transition
	switch state {
	case HalfOpen, Closed:
		atomic.StoreInt64(&b.failures, 0)
		atomic.StoreInt64(&b.successes, 0)
		atomic.StoreInt32(&b.halfOpenRequests, 0)
	}

	// Make callback if it exists
	if b.config.OnStateChange != nil {
		b.config.OnStateChange(oldState, state, reason)
	}

	return true
}
