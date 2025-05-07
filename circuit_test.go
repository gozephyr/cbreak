package cbreak

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TransientError represents a temporary error that should not trigger the circuit breaker
type TransientError struct {
	msg string
}

func (e TransientError) Error() string {
	return e.msg
}

// PermanentError represents a permanent error that should trigger the circuit breaker
type PermanentError struct {
	msg string
}

func (e PermanentError) Error() string {
	return e.msg
}

// TestCircuitInitialization verifies that a circuit breaker initializes with correct default settings
func TestCircuitInitialization(t *testing.T) {
	breaker, err := NewBreaker[string](DefaultConfig("test"))
	require.NoError(t, err)
	require.Equal(t, Closed, breaker.GetState())

	metrics := breaker.GetMetrics()
	require.Equal(t, int64(0), metrics.TotalRequests)
	require.Equal(t, int64(0), metrics.FailedCalls)
	require.Equal(t, int64(0), metrics.SuccessfulCalls)
}

// TestCircuitStateTransitions verifies all state transitions and their triggers
func TestCircuitStateTransitions(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 2
	config.SuccessThreshold = 2
	config.Timeout = 100 * time.Millisecond
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	ctx := context.Background()
	expectedErr := errors.New("test error")

	// Test Closed -> Open transition
	for i := 0; i < 2; i++ {
		_, err = breaker.Execute(ctx, func() (string, error) {
			return "", expectedErr
		})
		require.ErrorIs(t, err, expectedErr)
	}
	require.Equal(t, Open, breaker.GetState())

	// Test Open -> HalfOpen transition
	require.Eventually(t, func() bool {
		return breaker.GetState() == HalfOpen
	}, 200*time.Millisecond, 10*time.Millisecond)

	// Test HalfOpen -> Closed transition
	for i := 0; i < 2; i++ {
		result, err := breaker.Execute(ctx, func() (string, error) {
			return "success", nil
		})
		require.NoError(t, err)
		require.Equal(t, "success", result)
	}
	require.Eventually(t, func() bool {
		return breaker.GetState() == Closed
	}, 200*time.Millisecond, 10*time.Millisecond)

	// Test HalfOpen -> Open transition
	_, err = breaker.Execute(ctx, func() (string, error) {
		return "", expectedErr
	})
	require.ErrorIs(t, err, expectedErr)
	require.Equal(t, Open, breaker.GetState())
}

// TestConcurrentOperations verifies thread safety and concurrent operation handling
func TestConcurrentOperations(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 10        // Allow more failures before opening
	config.FailureRateThreshold = 100.0 // Disable failure rate threshold
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	var wg sync.WaitGroup
	var successCount, failureCount atomic.Int64
	ctx := context.Background()

	// Launch concurrent operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := breaker.Execute(ctx, func() (string, error) {
				if i%2 == 0 {
					return "success", nil
				}
				return "", errors.New("test error")
			})
			if err == nil {
				successCount.Add(1)
				require.Equal(t, "success", result)
			} else {
				failureCount.Add(1)
			}
		}(i)
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond) // Wait for metrics to update
	metrics := breaker.GetMetrics()
	require.Equal(t, int64(5), metrics.SuccessfulCalls)
	require.Equal(t, int64(5), metrics.FailedCalls)
}

// TestErrorClassification verifies error handling and classification
func TestErrorClassification(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 1
	config.ErrorClassifier = func(err error) bool {
		var permanentErr PermanentError
		return errors.As(err, &permanentErr)
	}
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	ctx := context.Background()

	// Test transient error
	_, err = breaker.Execute(ctx, func() (string, error) {
		return "", TransientError{msg: "temporary error"}
	})
	require.Error(t, err)
	require.Equal(t, Closed, breaker.GetState())

	// Test permanent error
	_, err = breaker.Execute(ctx, func() (string, error) {
		return "", PermanentError{msg: "permanent error"}
	})
	require.Error(t, err)
	require.Equal(t, Open, breaker.GetState())
}

// TestFallback verifies fallback mechanism
func TestFallback(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 1
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := breaker.ExecuteWithFallback(ctx,
		func() (string, error) {
			return "", errors.New("primary error")
		},
		func(err error) (string, error) {
			return "fallback", nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, "fallback", result)
}

// ExampleNewBreaker demonstrates how to create a new circuit breaker
func ExampleNewBreaker() {
	config := DefaultConfig("example")
	breaker, _ := NewBreaker[string](config)
	result, err := breaker.Execute(context.Background(), func() (string, error) {
		return "success", nil
	})
	if err == nil {
		_ = result
	}
}

// ExampleBreaker_ExecuteWithFallback demonstrates how to use fallback mechanism
func ExampleBreaker_ExecuteWithFallback() {
	config := DefaultConfig("example")
	breaker, _ := NewBreaker[string](config)
	result, err := breaker.ExecuteWithFallback(context.Background(),
		func() (string, error) {
			return "", errors.New("primary error")
		},
		func(err error) (string, error) {
			return "fallback", nil
		},
	)
	if err == nil {
		_ = result
	}
}

// ExampleGet demonstrates how to use the default registry
func ExampleGet() {
	breaker, _ := Get[string]("example")
	result, err := breaker.Execute(context.Background(), func() (string, error) {
		return "success", nil
	})
	if err == nil {
		_ = result
	}
}

// TestStateString verifies state string representation
func TestStateString(t *testing.T) {
	require.Equal(t, "closed", Closed.String())
	require.Equal(t, "open", Open.String())
	require.Equal(t, "half-open", HalfOpen.String())
}

// TestStateTransitions verifies state transition logic
func TestStateTransitions(t *testing.T) {
	config := DefaultConfig("test")
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	// Test manual state transitions
	breaker.SetState(Open, "test transition")
	require.Equal(t, Open, breaker.GetState())

	breaker.SetState(HalfOpen, "test transition")
	require.Equal(t, HalfOpen, breaker.GetState())

	breaker.SetState(Closed, "test transition")
	require.Equal(t, Closed, breaker.GetState())
}

// TestShutdown verifies shutdown behavior
func TestShutdown(t *testing.T) {
	config := DefaultConfig("test")
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	// Execute some operations
	ctx := context.Background()
	_, err = breaker.Execute(ctx, func() (string, error) {
		return "success", nil
	})
	require.NoError(t, err)

	// Shutdown the breaker
	breaker.Shutdown()

	// Verify metrics are preserved
	metrics := breaker.GetMetrics()
	require.Equal(t, int64(1), metrics.SuccessfulCalls)
}

// TestConfigValidation verifies configuration validation
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      *Config
		expectError bool
	}{
		{
			name:        "nil config",
			config:      nil,
			expectError: true,
		},
		{
			name: "empty name",
			config: &Config{
				Name: "",
			},
			expectError: true,
		},
		{
			name: "negative failure threshold",
			config: &Config{
				Name:             "test",
				FailureThreshold: -1,
			},
			expectError: true,
		},
		{
			name: "negative success threshold",
			config: &Config{
				Name:             "test",
				SuccessThreshold: -1,
			},
			expectError: true,
		},
		{
			name: "negative timeout",
			config: &Config{
				Name:    "test",
				Timeout: -1,
			},
			expectError: true,
		},
		{
			name: "valid config",
			config: &Config{
				Name:                "test",
				FailureThreshold:    5,
				SuccessThreshold:    3,
				Timeout:             time.Second,
				CommandTimeout:      time.Second,
				HalfOpenMaxRequests: 5,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewBreaker[string](tt.config)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestExecuteWithFallback verifies fallback mechanism with different scenarios
func TestExecuteWithFallback(t *testing.T) {
	config := DefaultConfig("test")
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	ctx := context.Background()

	// Test successful primary operation
	result, err := breaker.ExecuteWithFallback(ctx,
		func() (string, error) {
			return "primary", nil
		},
		func(err error) (string, error) {
			return "fallback", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "primary", result)

	// Test fallback with error
	result, err = breaker.ExecuteWithFallback(ctx,
		func() (string, error) {
			return "", errors.New("primary error")
		},
		func(err error) (string, error) {
			return "fallback", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "fallback", result)

	// Test both operations failing
	_, err = breaker.ExecuteWithFallback(ctx,
		func() (string, error) {
			return "", errors.New("primary error")
		},
		func(err error) (string, error) {
			return "", errors.New("fallback error")
		},
	)
	require.Error(t, err)
	require.Equal(t, "fallback error", err.Error())
}

// TestStateStringUnknown verifies unknown state string representation
func TestStateStringUnknown(t *testing.T) {
	var unknownState State = 999
	require.Equal(t, "unknown", unknownState.String())
}

// TestExecuteErrorPaths verifies error handling in Execute
func TestExecuteErrorPaths(t *testing.T) {
	config := DefaultConfig("test")
	config.CommandTimeout = 50 * time.Millisecond
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	ctx := context.Background()

	// Test nil function
	_, err = breaker.Execute(ctx, nil)
	require.Equal(t, ErrNilFunction, err)

	// Test timeout
	_, err = breaker.Execute(ctx, func() (string, error) {
		time.Sleep(100 * time.Millisecond)
		return "success", nil
	})
	require.Equal(t, context.DeadlineExceeded, err)

	// Test panic recovery
	_, err = breaker.Execute(ctx, func() (string, error) {
		panic("test panic")
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "panic in execution")

	// Test context cancellation
	cancelCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err = breaker.Execute(cancelCtx, func() (string, error) {
		time.Sleep(100 * time.Millisecond)
		return "success", nil
	})
	require.Equal(t, context.Canceled, err)
}

// TestNewBreakerEdgeCases tests edge cases for NewBreaker
func TestNewBreakerEdgeCases(t *testing.T) {
	// Test with nil config
	_, err := NewBreaker[string](nil)
	require.Error(t, err)
	require.Equal(t, ErrInvalidConfig, err)

	// Test with empty name
	config := &Config{Name: ""}
	_, err = NewBreaker[string](config)
	require.Error(t, err)

	// Test with negative thresholds
	config = &Config{
		Name:             "test",
		FailureThreshold: -1,
	}
	_, err = NewBreaker[string](config)
	require.Error(t, err)

	config = &Config{
		Name:             "test",
		SuccessThreshold: -1,
	}
	_, err = NewBreaker[string](config)
	require.Error(t, err)

	// Test with negative timeout
	config = &Config{
		Name:    "test",
		Timeout: -1,
	}
	_, err = NewBreaker[string](config)
	require.Error(t, err)
}

// TestExecuteEdgeCases tests edge cases for Execute
func TestExecuteEdgeCases(t *testing.T) {
	config := DefaultConfig("test")
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	// Test with nil function
	_, err = breaker.Execute(context.Background(), nil)
	require.Error(t, err)
	require.Equal(t, ErrNilFunction, err)

	// Test with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = breaker.Execute(ctx, func() (string, error) {
		return "test", nil
	})
	require.Error(t, err)
	require.Equal(t, context.Canceled, err)

	// Test with deadline exceeded
	ctx, cancel = context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(1 * time.Millisecond)
	_, err = breaker.Execute(ctx, func() (string, error) {
		return "test", nil
	})
	require.Error(t, err)
	require.Equal(t, context.DeadlineExceeded, err)
}

// TestStateTransitionsEdgeCases tests edge cases for state transitions
func TestStateTransitionsEdgeCases(t *testing.T) {
	config := DefaultConfig("test")
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	// Test invalid state transition
	breaker.SetState(Open, "test")
	breaker.SetState(Open, "test") // Same state
	require.Equal(t, Open, breaker.GetState())

	// Test state transition with nil reason
	breaker.SetState(Closed, "")
	require.Equal(t, Closed, breaker.GetState())

	// Test state transition with long reason
	longReason := strings.Repeat("a", 1000)
	breaker.SetState(HalfOpen, longReason)
	require.Equal(t, HalfOpen, breaker.GetState())
}

// TestShouldTransitionToOpenEdgeCases tests edge cases for shouldTransitionToOpen
func TestShouldTransitionToOpenEdgeCases(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 2
	config.FailureRateThreshold = 50.0
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	// Test with no failures
	require.False(t, breaker.shouldTransitionToOpen(
		0, // failures
		0, // failureRate
		0, // totalCalls
		Closed,
	))

	// Test with exactly failure threshold
	require.True(t, breaker.shouldTransitionToOpen(
		2,
		100, // failureRate
		2,   // totalCalls
		Closed,
	))

	// Test with failure rate threshold
	require.True(t, breaker.shouldTransitionToOpen(
		5, // failures
		50.0,
		10, // totalCalls >= 5
		Closed,
	))

	// Test with failure rate threshold but not enough calls
	require.False(t, breaker.shouldTransitionToOpen(
		1,    // failures
		33.3, // failureRate < FailureRateThreshold
		3,    // totalCalls < 5
		Closed,
	))

	// Test with HalfOpen state
	require.True(t, breaker.shouldTransitionToOpen(
		1,    // failures
		50.0, // failureRate
		2,    // totalCalls
		HalfOpen,
	), "Should transition to open on any failure in half-open state")
}

// TestValidateConfigEdgeCases tests edge cases for validateConfig
func TestValidateConfigEdgeCases(t *testing.T) {
	// Test with nil config
	err := validateConfig(nil)
	require.Error(t, err)
	require.Equal(t, ErrInvalidConfig, err)

	// Test with empty name
	err = validateConfig(&Config{Name: ""})
	require.Error(t, err)

	// Test with negative failure threshold
	err = validateConfig(&Config{
		Name:             "test",
		FailureThreshold: -1,
	})
	require.Error(t, err)

	// Test with negative success threshold
	err = validateConfig(&Config{
		Name:             "test",
		SuccessThreshold: -1,
	})
	require.Error(t, err)

	// Test with negative timeout
	err = validateConfig(&Config{
		Name:    "test",
		Timeout: -1,
	})
	require.Error(t, err)

	// Test with negative failure rate threshold
	err = validateConfig(&Config{
		Name:                 "test",
		FailureRateThreshold: -1,
	})
	require.Error(t, err)

	// Test with invalid failure rate threshold
	err = validateConfig(&Config{
		Name:                 "test",
		FailureRateThreshold: 101,
	})
	require.Error(t, err)
}
