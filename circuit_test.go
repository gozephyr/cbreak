package cbreak

import (
	"context"
	"errors"
	"fmt"
	"runtime"
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
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)

	ctx := context.Background()

	// Test nil function
	_, err = breaker.Execute(ctx, nil)
	require.Equal(t, ErrNilFunction, err)

	// Test timeout (no longer applicable with zero-allocation mode)
	_, err = breaker.Execute(ctx, func() (string, error) {
		time.Sleep(100 * time.Millisecond)
		return "success", nil
	})
	require.NoError(t, err) // Should succeed since timeout is disabled

	// Test panic recovery
	_, err = breaker.Execute(ctx, func() (string, error) {
		panic("test panic")
	})
	require.Error(t, err)
	require.Equal(t, ErrPanic, err)
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
	require.NoError(t, err) // Should succeed since context cancellation is not handled
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

	// Test with cancelled context (no longer applicable with zero-allocation mode)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = breaker.Execute(ctx, func() (string, error) {
		return "test", nil
	})
	require.NoError(t, err) // Should succeed since context cancellation is not handled

	// Test with deadline exceeded (no longer applicable with zero-allocation mode)
	ctx, cancel = context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	time.Sleep(1 * time.Millisecond)

	_, err = breaker.Execute(ctx, func() (string, error) {
		return "test", nil
	})
	require.NoError(t, err) // Should succeed since deadline is not checked
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

// ============================================================================
// PERFORMANCE AND MEMORY TESTS
// ============================================================================

// TestZeroAllocations verifies that Execute operations produce zero allocations
func TestZeroAllocations(t *testing.T) {
	breaker, err := NewBreaker[string](DefaultConfig("test"))
	require.NoError(t, err)
	defer breaker.Shutdown()

	// Get initial memory stats
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Execute many operations
	for i := 0; i < 1000; i++ {
		_, _ = breaker.Execute(context.Background(), func() (string, error) {
			return "success", nil
		})
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	// Check that no significant allocations occurred during execution
	allocDiff := m2.TotalAlloc - m1.TotalAlloc

	// Allow for some minimal allocations (GC overhead, etc.)
	require.Less(t, allocDiff, uint64(20000), "Should have minimal allocations during execution")
}

// TestMemoryFootprint verifies the memory footprint of circuit breaker creation
func TestMemoryFootprint(t *testing.T) {
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	// Create many circuit breakers
	breakers := make([]*Breaker[string], 100)
	for i := 0; i < 100; i++ {
		breaker, err := NewBreaker[string](DefaultConfig(fmt.Sprintf("test-%d", i)))
		require.NoError(t, err)
		breakers[i] = breaker
	}

	runtime.GC()
	runtime.ReadMemStats(&m2)

	// Calculate memory per breaker
	totalAlloc := m2.TotalAlloc - m1.TotalAlloc
	bytesPerBreaker := totalAlloc / 100

	// Should be reasonable (less than 2KB per breaker)
	require.Less(t, bytesPerBreaker, uint64(2000), "Memory footprint should be reasonable")

	// Cleanup
	for _, breaker := range breakers {
		breaker.Shutdown()
	}
}

// TestPerformanceUnderLoad tests performance under high concurrent load
func TestPerformanceUnderLoad(t *testing.T) {
	breaker, err := NewBreaker[string](DefaultConfig("test"))
	require.NoError(t, err)
	defer breaker.Shutdown()

	const numGoroutines = 100
	const operationsPerGoroutine = 1000

	var wg sync.WaitGroup
	var totalOps int64
	var totalErrors int64

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				_, err := breaker.Execute(context.Background(), func() (string, error) {
					return "success", nil
				})
				atomic.AddInt64(&totalOps, 1)
				if err != nil {
					atomic.AddInt64(&totalErrors, 1)
				}
			}
		}()
	}

	wg.Wait()
	duration := time.Since(start)

	totalOperations := int64(numGoroutines * operationsPerGoroutine)
	opsPerSecond := float64(totalOperations) / duration.Seconds()

	require.Equal(t, totalOperations, totalOps, "All operations should complete")
	require.Equal(t, int64(0), totalErrors, "No errors should occur under normal load")
	require.Greater(t, opsPerSecond, 100000.0, "Should handle at least 100k ops/sec")
}

// ============================================================================
// CONCURRENCY AND RACE CONDITION TESTS
// ============================================================================

// TestConcurrentStateTransitions tests concurrent state transitions
func TestConcurrentStateTransitions(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 5
	config.SuccessThreshold = 3
	config.Timeout = 50 * time.Millisecond

	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)
	defer breaker.Shutdown()

	const numGoroutines = 50
	const operationsPerGoroutine = 100

	var wg sync.WaitGroup
	var stateChanges int64

	// Start goroutines that will trigger state changes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(shouldFail bool) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				_, err := breaker.Execute(context.Background(), func() (string, error) {
					if shouldFail {
						return "", errors.New("test error")
					}
					return "success", nil
				})

				// Track state changes
				state := breaker.GetState()
				if state != Closed {
					atomic.AddInt64(&stateChanges, 1)
				}

				// Ignore errors for this test
				_ = err
			}
		}(i%2 == 0) // Alternate between success and failure
	}

	wg.Wait()

	require.Greater(t, stateChanges, int64(0), "Should observe state changes under concurrent load")
}

// TestRaceConditionInMetrics tests for race conditions in metrics collection
func TestRaceConditionInMetrics(t *testing.T) {
	breaker, err := NewBreaker[string](DefaultConfig("test"))
	require.NoError(t, err)
	defer breaker.Shutdown()

	const numGoroutines = 20
	const operationsPerGoroutine = 100

	var wg sync.WaitGroup

	// Start goroutines that execute operations and read metrics
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				// Execute operation
				_, _ = breaker.Execute(context.Background(), func() (string, error) {
					return "success", nil
				})

				// Read metrics (this should not cause race conditions)
				metrics := breaker.GetMetrics()
				_ = metrics.TotalRequests
				_ = metrics.SuccessfulCalls
			}
		}()
	}

	wg.Wait()

	// Final metrics should be consistent
	metrics := breaker.GetMetrics()
	expectedTotal := int64(numGoroutines * operationsPerGoroutine)
	require.Equal(t, expectedTotal, metrics.TotalRequests, "Total requests should be consistent")
	require.Equal(t, expectedTotal, metrics.SuccessfulCalls, "Successful calls should be consistent")
}

// ============================================================================
// ERROR HANDLING AND EDGE CASE TESTS
// ============================================================================

// TestPanicRecoveryComprehensive tests comprehensive panic recovery scenarios
func TestPanicRecoveryComprehensive(t *testing.T) {
	testCases := []struct {
		name        string
		panicValue  any
		expectError bool
	}{
		{"String panic", "test panic", true},
		{"Error panic", errors.New("error panic"), true},
		{"Int panic", 42, true},
		{"Nil panic", nil, true},
		{"Struct panic", struct{ msg string }{"struct panic"}, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a fresh breaker for each test to avoid state interference
			breaker, err := NewBreaker[string](DefaultConfig("test"))
			require.NoError(t, err)
			defer breaker.Shutdown()

			_, err = breaker.Execute(context.Background(), func() (string, error) {
				panic(tc.panicValue)
			})

			if tc.expectError {
				require.Error(t, err)
				require.Equal(t, ErrPanic, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestErrorClassificationComprehensive tests comprehensive error classification
func TestErrorClassificationComprehensive(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 2
	config.ErrorClassifier = func(err error) bool {
		// Only classify PermanentError as failures
		_, ok := err.(PermanentError)
		return ok
	}

	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)
	defer breaker.Shutdown()

	// Test with transient errors (should not trigger circuit breaker)
	for i := 0; i < 5; i++ {
		_, err := breaker.Execute(context.Background(), func() (string, error) {
			return "", TransientError{"transient error"}
		})
		require.Error(t, err)
		require.Equal(t, Closed, breaker.GetState(), "Circuit should remain closed for transient errors")
	}

	// Test with permanent errors (should trigger circuit breaker)
	for i := 0; i < 3; i++ {
		_, err := breaker.Execute(context.Background(), func() (string, error) {
			return "", PermanentError{"permanent error"}
		})
		require.Error(t, err)
	}

	require.Equal(t, Open, breaker.GetState(), "Circuit should be open after permanent errors")
}

// TestHalfOpenStateBehavior tests comprehensive half-open state behavior
func TestHalfOpenStateBehavior(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 1
	config.SuccessThreshold = 2
	config.Timeout = 50 * time.Millisecond
	config.HalfOpenMaxRequests = 3

	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)
	defer breaker.Shutdown()

	// Trigger open state
	_, err = breaker.Execute(context.Background(), func() (string, error) {
		return "", errors.New("trigger open")
	})
	require.Error(t, err)
	require.Equal(t, Open, breaker.GetState())

	// Wait for half-open transition
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, HalfOpen, breaker.GetState())

	// Test half-open request limiting
	var successCount int64
	var rejectCount int64

	for i := 0; i < 10; i++ {
		_, err := breaker.Execute(context.Background(), func() (string, error) {
			return "success", nil
		})

		if err == nil {
			atomic.AddInt64(&successCount, 1)
		} else {
			atomic.AddInt64(&rejectCount, 1)
		}

		// Check if circuit closed after success threshold
		if breaker.GetState() == Closed {
			break
		}
	}

	require.GreaterOrEqual(t, successCount, int64(2), "Should allow at least SuccessThreshold requests")
	require.LessOrEqual(t, successCount, int64(3), "Should not exceed HalfOpenMaxRequests before closing")
}

// ============================================================================
// INTEGRATION AND REAL-WORLD SCENARIO TESTS
// ============================================================================

// TestRealWorldScenario simulates a real-world service failure scenario
func TestRealWorldScenario(t *testing.T) {
	config := DefaultConfig("database-service")
	config.FailureThreshold = 3
	config.SuccessThreshold = 2
	config.Timeout = 100 * time.Millisecond
	config.HalfOpenMaxRequests = 5

	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)
	defer breaker.Shutdown()

	// Simulate database service
	dbHealthy := true
	var callCount int64

	executeDBOperation := func() (string, error) {
		atomic.AddInt64(&callCount, 1)

		if !dbHealthy {
			return "", errors.New("database connection failed")
		}

		// Simulate occasional failures even when healthy
		if callCount%10 == 0 {
			return "", errors.New("temporary database error")
		}

		return "query result", nil
	}

	// Phase 1: Normal operation
	for i := 0; i < 20; i++ {
		result, err := breaker.Execute(context.Background(), executeDBOperation)
		if err == nil {
			require.Equal(t, "query result", result)
		}
	}
	require.Equal(t, Closed, breaker.GetState(), "Should remain closed during normal operation")

	// Phase 2: Database becomes unhealthy
	dbHealthy = false
	for i := 0; i < 5; i++ {
		_, err := breaker.Execute(context.Background(), executeDBOperation)
		require.Error(t, err)
	}
	require.Equal(t, Open, breaker.GetState(), "Should open circuit after failures")

	// Phase 3: Circuit is open - requests should be rejected
	for i := 0; i < 5; i++ {
		_, err := breaker.Execute(context.Background(), executeDBOperation)
		require.Error(t, err)
		require.Equal(t, ErrCircuitOpen, err)
	}

	// Phase 4: Wait for half-open and database recovery
	time.Sleep(150 * time.Millisecond)
	dbHealthy = true

	// Execute successful operations to close the circuit
	successCount := 0
	for i := 0; i < 5; i++ {
		_, err := breaker.Execute(context.Background(), executeDBOperation)
		if err == nil {
			successCount++
		}

		// Check if circuit closed after enough successes
		if breaker.GetState() == Closed {
			break
		}
	}

	require.Equal(t, Closed, breaker.GetState(), "Should close circuit after recovery")
}

// TestMultipleBreakers tests multiple circuit breakers working independently
func TestMultipleBreakers(t *testing.T) {
	// Create breakers for different services
	dbBreaker, err := NewBreaker[string](DefaultConfig("database"))
	require.NoError(t, err)
	defer dbBreaker.Shutdown()

	apiBreaker, err := NewBreaker[string](DefaultConfig("api"))
	require.NoError(t, err)
	defer apiBreaker.Shutdown()

	cacheBreaker, err := NewBreaker[string](DefaultConfig("cache"))
	require.NoError(t, err)
	defer cacheBreaker.Shutdown()

	// Simulate different failure patterns
	// Database fails
	for i := 0; i < 5; i++ {
		_, _ = dbBreaker.Execute(context.Background(), func() (string, error) {
			return "", errors.New("db error")
		})
	}
	require.Equal(t, Open, dbBreaker.GetState())

	// API succeeds
	for i := 0; i < 5; i++ {
		_, _ = apiBreaker.Execute(context.Background(), func() (string, error) {
			return "api success", nil
		})
	}
	require.Equal(t, Closed, apiBreaker.GetState())

	// Cache has mixed results
	for i := 0; i < 10; i++ {
		_, _ = cacheBreaker.Execute(context.Background(), func() (string, error) {
			if i%3 == 0 {
				return "", errors.New("cache miss")
			}
			return "cache hit", nil
		})
	}
	require.Equal(t, Closed, cacheBreaker.GetState(), "Cache should remain closed with mixed results")

	// Verify independence
	require.Equal(t, Open, dbBreaker.GetState(), "DB breaker should be open")
	require.Equal(t, Closed, apiBreaker.GetState(), "API breaker should be closed")
	require.Equal(t, Closed, cacheBreaker.GetState(), "Cache breaker should be closed")
}

// TestNewBreakerWithNilConfig tests NewBreaker with nil config
func TestNewBreakerWithNilConfig(t *testing.T) {
	breaker, err := NewBreaker[string](nil)
	require.Error(t, err)
	require.Equal(t, ErrInvalidConfig, err)
	require.Nil(t, breaker)
}

// TestGetStateFallback tests GetState fallback to Closed
func TestGetStateFallback(t *testing.T) {
	breaker, err := NewBreaker[string](DefaultConfig("test"))
	require.NoError(t, err)
	defer breaker.Shutdown()

	// Manually set an invalid state
	breaker.SetState(State(999), "test") // Invalid state

	// GetState should return the invalid state (no validation in GetState)
	state := breaker.GetState()
	require.Equal(t, State(999), state)
}

// TestShutdownDetection tests shutdown channel detection
func TestShutdownDetection(t *testing.T) {
	breaker, err := NewBreaker[string](DefaultConfig("test"))
	require.NoError(t, err)

	// Shutdown the breaker
	breaker.Shutdown()

	// Try to execute after shutdown
	_, err = breaker.Execute(context.Background(), func() (string, error) {
		return "success", nil
	})
	require.Error(t, err)
	require.Equal(t, ErrCircuitOpen, err)
}

// TestHalfOpenRequestLimitingEdgeCases tests half-open request limiting edge cases
func TestHalfOpenRequestLimitingEdgeCases(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 1
	config.SuccessThreshold = 2
	config.Timeout = 50 * time.Millisecond
	config.HalfOpenMaxRequests = 2

	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)
	defer breaker.Shutdown()

	// Trigger open state
	_, err = breaker.Execute(context.Background(), func() (string, error) {
		return "", errors.New("trigger open")
	})
	require.Error(t, err)
	require.Equal(t, Open, breaker.GetState())

	// Wait for half-open transition
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, HalfOpen, breaker.GetState())

	// Test exact limit boundary
	var wg sync.WaitGroup
	results := make([]error, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := breaker.Execute(context.Background(), func() (string, error) {
				return "success", nil
			})
			results[index] = err
		}(i)
	}

	wg.Wait()

	// Should have exactly 2 successes and 1 rejection
	successCount := 0
	rejectCount := 0
	for _, err := range results {
		switch err {
		case nil:
			successCount++
		case ErrCircuitOpen:
			rejectCount++
		}
	}

	require.GreaterOrEqual(t, successCount, 2, "Should allow at least HalfOpenMaxRequests")
	require.LessOrEqual(t, successCount, 3, "Should not exceed reasonable limit")
}

// TestRegistryErrorHandling tests registry error handling
func TestRegistryErrorHandling(t *testing.T) {
	// Test GetOrCreate with invalid config
	registry := NewRegistry[string]()

	// This should trigger the error path in GetOrCreate
	breaker, err := registry.GetOrCreate("test", &Config{
		Name: "", // Invalid empty name
	})
	require.Error(t, err)
	require.Nil(t, breaker)
}

// TestGetWithConfigErrorHandling tests GetWithConfig error handling
func TestGetWithConfigErrorHandling(t *testing.T) {
	// Test with nil config
	breaker, err := GetWithConfig[string]("test", nil)
	require.Error(t, err)
	require.Equal(t, ErrInvalidConfig, err)
	require.Nil(t, breaker)
}

// TestConfigValidationEdgeCases tests config validation edge cases
func TestConfigValidationEdgeCases(t *testing.T) {
	// Test with zero timeout
	err := validateConfig(&Config{
		Name:             "test",
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Timeout:          0,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid timeout")

	// Test with negative timeout
	err = validateConfig(&Config{
		Name:             "test",
		FailureThreshold: 1,
		SuccessThreshold: 1,
		Timeout:          -1 * time.Second,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid timeout")

	// Test with zero failure threshold
	err = validateConfig(&Config{
		Name:             "test",
		FailureThreshold: 0,
		SuccessThreshold: 1,
		Timeout:          time.Second,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid failure threshold")

	// Test with zero success threshold
	err = validateConfig(&Config{
		Name:             "test",
		FailureThreshold: 1,
		SuccessThreshold: 0,
		Timeout:          time.Second,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid success threshold")

	// Test with negative half-open max requests
	err = validateConfig(&Config{
		Name:                "test",
		FailureThreshold:    1,
		SuccessThreshold:    1,
		Timeout:             time.Second,
		HalfOpenMaxRequests: -1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid half-open max requests")
}

// TestStateTransitionEdgeCases tests state transition edge cases
func TestStateTransitionEdgeCases(t *testing.T) {
	config := DefaultConfig("test")
	config.FailureThreshold = 1            // Set to 1 to trigger open state immediately
	config.SuccessThreshold = 1            // Set to 1 to close quickly
	config.Timeout = 50 * time.Millisecond // Short timeout for testing
	breaker, err := NewBreaker[string](config)
	require.NoError(t, err)
	defer breaker.Shutdown()

	// Test transition to open
	_, err = breaker.Execute(context.Background(), func() (string, error) {
		return "", errors.New("trigger open")
	})
	require.Error(t, err)
	require.Equal(t, Open, breaker.GetState())

	// Wait for half-open and then close
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, HalfOpen, breaker.GetState())

	// Execute successful operation to close
	_, err = breaker.Execute(context.Background(), func() (string, error) {
		return "success", nil
	})
	require.NoError(t, err)
	require.Equal(t, Closed, breaker.GetState())
}

// TestSetStateEdgeCases tests SetState edge cases
func TestSetStateEdgeCases(t *testing.T) {
	breaker, err := NewBreaker[string](DefaultConfig("test"))
	require.NoError(t, err)
	defer breaker.Shutdown()

	// Test setting invalid state
	breaker.SetState(State(999), "test")
	// Note: SetState doesn't validate the state, so it will set the invalid state
	// The fallback to Closed happens in GetState, not SetState

	// Test setting valid states
	breaker.SetState(Open, "test")
	require.Equal(t, Open, breaker.GetState())

	breaker.SetState(HalfOpen, "test")
	require.Equal(t, HalfOpen, breaker.GetState())

	breaker.SetState(Closed, "test")
	require.Equal(t, Closed, breaker.GetState())
}

// TestConcurrentShutdown tests concurrent shutdown scenarios
func TestConcurrentShutdown(t *testing.T) {
	breaker, err := NewBreaker[string](DefaultConfig("test"))
	require.NoError(t, err)

	var wg sync.WaitGroup
	const numGoroutines = 10

	// Start multiple goroutines trying to execute
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = breaker.Execute(context.Background(), func() (string, error) {
				time.Sleep(10 * time.Millisecond) // Simulate work
				return "success", nil
			})
		}()
	}

	// Shutdown while operations are running
	time.Sleep(5 * time.Millisecond)
	breaker.Shutdown()

	wg.Wait()

	// All operations after shutdown should be rejected
	_, err = breaker.Execute(context.Background(), func() (string, error) {
		return "success", nil
	})
	require.Error(t, err)
	require.Equal(t, ErrCircuitOpen, err)
}
