package cbreak

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var errRegistryTest = errors.New("test error")

// TestRegistryInitialization verifies registry creation and default registry
func TestRegistryInitialization(t *testing.T) {
	reg := NewRegistry[string]()
	require.NotNil(t, reg, "Registry should not be nil")

	defaultReg := DefaultRegistry[string]()
	require.NotNil(t, defaultReg, "Default registry should not be nil")
}

// TestCircuitRegistration verifies circuit breaker registration and retrieval
func TestCircuitRegistration(t *testing.T) {
	reg := NewRegistry[string]()
	config := DefaultConfig("test-circuit")

	// Test GetOrCreate
	breaker, err := reg.GetOrCreate("test-circuit", config)
	require.NoError(t, err)
	require.NotNil(t, breaker, "Breaker should not be nil")

	// Test getting existing circuit
	sameBreaker, err := reg.GetOrCreate("test-circuit", config)
	require.NoError(t, err)
	require.Equal(t, breaker, sameBreaker, "Should return the same breaker instance")

	// Test with different config
	newConfig := DefaultConfig("test-circuit")
	newConfig.FailureThreshold = 5
	differentBreaker, err := reg.GetOrCreate("test-circuit", newConfig)
	require.NoError(t, err)
	require.Equal(t, breaker, differentBreaker, "Should return existing breaker even with different config")
}

// TestCircuitRemoval verifies circuit breaker removal
func TestCircuitRemoval(t *testing.T) {
	reg := NewRegistry[string]()
	config := DefaultConfig("test-circuit")

	// Create and verify circuit exists
	breaker, err := reg.GetOrCreate("test-circuit", config)
	require.NoError(t, err)
	require.NotNil(t, breaker)

	// Remove circuit
	reg.Remove("test-circuit")

	// Verify circuit is removed
	newBreaker, err := reg.GetOrCreate("test-circuit", config)
	require.NoError(t, err)
	require.NotEqual(t, breaker, newBreaker, "Should create new breaker after removal")
}

// TestRegistryResetAll verifies reset functionality
func TestRegistryResetAll(t *testing.T) {
	reg := NewRegistry[string]()
	config := DefaultConfig("test-circuit")

	// Create circuit and trigger some failures
	breaker, err := reg.GetOrCreate("test-circuit", config)
	require.NoError(t, err)

	ctx := context.Background()

	for i := 0; i < config.FailureThreshold; i++ {
		_, err = breaker.Execute(ctx, func() (string, error) {
			return "", errRegistryTest
		})
		require.ErrorIs(t, err, errRegistryTest)
	}

	// Verify circuit is open
	require.Equal(t, Open, breaker.GetState())

	// Reset all circuits
	reg.ResetAll()

	// Verify circuit is reset
	require.Equal(t, Closed, breaker.GetState())
}

// TestGetStates verifies state retrieval
func TestGetStates(t *testing.T) {
	reg := NewRegistry[string]()
	config := DefaultConfig("test-circuit")

	// Create circuit and execute an operation
	breaker, err := reg.GetOrCreate("test-circuit", config)
	require.NoError(t, err)

	ctx := context.Background()
	_, err = breaker.Execute(ctx, func() (string, error) {
		return "test", nil
	})
	require.NoError(t, err)

	// Get states
	states := reg.GetStates()
	require.Contains(t, states, "test-circuit")
	require.Equal(t, Closed, states["test-circuit"])
}

// TestGetMetrics verifies metrics retrieval
func TestGetMetrics(t *testing.T) {
	reg := NewRegistry[string]()
	config := DefaultConfig("test-circuit")
	config.FailureThreshold = 2

	// Create circuit and execute some operations
	breaker, err := reg.GetOrCreate("test-circuit", config)
	require.NoError(t, err)

	ctx := context.Background()

	// Execute successful operation
	_, err = breaker.Execute(ctx, func() (string, error) {
		return "success", nil
	})
	require.NoError(t, err)

	// Execute failed operation
	_, err = breaker.Execute(ctx, func() (string, error) {
		return "", errRegistryTest
	})
	require.ErrorIs(t, err, errRegistryTest)

	// Get metrics
	metrics := reg.GetMetrics()
	require.Contains(t, metrics, "test-circuit")
	require.Equal(t, int64(1), metrics["test-circuit"].SuccessfulCalls)
	require.Equal(t, int64(1), metrics["test-circuit"].FailedCalls)
	require.Equal(t, int64(2), metrics["test-circuit"].TotalRequests)
	require.Equal(t, Closed, metrics["test-circuit"].State)

	// Create another circuit
	breaker2, err := reg.GetOrCreate("test-circuit-2", config)
	require.NoError(t, err)

	// Execute operations
	_, err = breaker2.Execute(ctx, func() (string, error) {
		return "success", nil
	})
	require.NoError(t, err)
	_, err = breaker2.Execute(ctx, func() (string, error) {
		return "", errRegistryTest
	})
	require.ErrorIs(t, err, errRegistryTest)

	// Get metrics
	metrics = reg.GetMetrics()
	require.Contains(t, metrics, "test-circuit-2")
	require.Equal(t, int64(1), metrics["test-circuit-2"].SuccessfulCalls)
	require.Equal(t, int64(1), metrics["test-circuit-2"].FailedCalls)
	require.Equal(t, int64(2), metrics["test-circuit-2"].TotalRequests)
	require.Equal(t, Closed, metrics["test-circuit-2"].State)

	// Test metrics after circuit state changes
	_, err = breaker2.Execute(ctx, func() (string, error) {
		return "", errRegistryTest
	})
	require.ErrorIs(t, err, errRegistryTest)

	// Circuit should be open now, next call should be rejected
	_, err = breaker2.Execute(ctx, func() (string, error) {
		return "", errRegistryTest
	})
	require.ErrorIs(t, err, ErrCircuitOpen)

	metrics = reg.GetMetrics()
	require.Contains(t, metrics, "test-circuit-2")
	require.Equal(t, Open, metrics["test-circuit-2"].State)
	require.Equal(t, int64(1), metrics["test-circuit-2"].SuccessfulCalls)
	require.Equal(t, int64(2), metrics["test-circuit-2"].FailedCalls)
	require.Equal(t, int64(1), metrics["test-circuit-2"].RejectedCalls)
	require.Equal(t, int64(4), metrics["test-circuit-2"].TotalRequests)

	// Test metrics after reset
	breaker2.Reset()

	metrics = reg.GetMetrics()
	require.Contains(t, metrics, "test-circuit-2")
	require.Equal(t, Closed, metrics["test-circuit-2"].State)
	require.Equal(t, int64(0), metrics["test-circuit-2"].SuccessfulCalls)
	require.Equal(t, int64(0), metrics["test-circuit-2"].FailedCalls)
	require.Equal(t, int64(0), metrics["test-circuit-2"].RejectedCalls)
	require.Equal(t, int64(0), metrics["test-circuit-2"].TotalRequests)
}

// TestConcurrentRegistryOperations verifies thread safety
func TestConcurrentRegistryOperations(t *testing.T) {
	reg := NewRegistry[string]()
	config := DefaultConfig("test-circuit")

	// Test concurrent GetOrCreate
	done := make(chan bool)

	for i := 0; i < 100; i++ {
		go func() {
			breaker, err := reg.GetOrCreate("test-circuit", config)
			require.NoError(t, err)
			require.NotNil(t, breaker)

			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 100; i++ {
		<-done
	}

	// Verify only one breaker was created
	breaker, err := reg.GetOrCreate("test-circuit", config)
	require.NoError(t, err)
	require.NotNil(t, breaker)
}

// TestRegistryWithDifferentTypes verifies type safety
func TestRegistryWithDifferentTypes(t *testing.T) {
	type CustomType struct {
		Value string
	}

	reg := NewRegistry[CustomType]()
	config := DefaultConfig("test-circuit")

	breaker, err := reg.GetOrCreate("test-circuit", config)
	require.NoError(t, err)
	require.NotNil(t, breaker)

	result, err := breaker.Execute(context.Background(), func() (CustomType, error) {
		return CustomType{Value: "test"}, nil
	})
	require.NoError(t, err)
	require.Equal(t, "test", result.Value)
}

// TestRegistryBasics verifies basic registry operations
func TestRegistryBasics(t *testing.T) {
	reg := NewRegistry[string]()
	defer reg.ResetAll() // Cleanup after test

	// Test GetOrCreate with nil config
	breaker, err := reg.GetOrCreate("test-circuit", nil)
	require.NoError(t, err)
	require.NotNil(t, breaker)

	// Test GetOrCreate with custom config
	config := DefaultConfig("test-circuit-2")
	config.FailureThreshold = 5
	breaker2, err := reg.GetOrCreate("test-circuit-2", config)
	require.NoError(t, err)
	require.NotNil(t, breaker2)

	// Test Remove
	reg.Remove("test-circuit")
	breaker3, err := reg.GetOrCreate("test-circuit", nil)
	require.NoError(t, err)
	require.NotEqual(t, breaker, breaker3, "Should create new breaker after removal")
}

// TestRegistryConcurrentAccess verifies thread safety of registry operations
func TestRegistryConcurrentAccess(t *testing.T) {
	reg := NewRegistry[string]()
	defer reg.ResetAll() // Cleanup after test

	var wg sync.WaitGroup

	iterations := 100

	wg.Add(iterations)

	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()

			breaker, err := reg.GetOrCreate("test-circuit", nil)
			require.NoError(t, err)
			require.NotNil(t, breaker)
		}()
	}

	wg.Wait()

	// Verify only one breaker was created
	breaker, err := reg.GetOrCreate("test-circuit", nil)
	require.NoError(t, err)
	require.NotNil(t, breaker)
}

// TestRegistryExecute verifies circuit breaker execution through registry
func TestRegistryExecute(t *testing.T) {
	reg := NewRegistry[string]()
	defer reg.ResetAll() // Cleanup after test

	breaker, err := reg.GetOrCreate("test-circuit", nil)
	require.NoError(t, err)

	ctx := context.Background()
	result, err := breaker.Execute(ctx, func() (string, error) {
		return "success", nil
	})
	require.NoError(t, err)
	require.Equal(t, "success", result)
}

// TestDefaultRegistryFunctions verifies the global registry functions
func TestDefaultRegistryFunctions(t *testing.T) {
	defer ResetAll[string]() // Cleanup after test

	// Test Get
	breaker, err := Get[string]("test-circuit")
	require.NoError(t, err)
	require.NotNil(t, breaker)

	// Test GetWithConfig
	config := DefaultConfig("test-circuit-2")
	breaker2, err := GetWithConfig[string]("test-circuit-2", config)
	require.NoError(t, err)
	require.NotNil(t, breaker2)

	// Test Remove
	Remove[string]("test-circuit")
	breaker3, err := Get[string]("test-circuit")
	require.NoError(t, err)
	require.NotEqual(t, breaker, breaker3, "Should create new breaker after removal")

	// Test GetStates
	states := GetStates[string]()
	require.Contains(t, states, "test-circuit-2")
	require.Equal(t, Closed, states["test-circuit-2"])

	// Test GetMetrics
	metrics := GetMetrics[string]()
	require.Contains(t, metrics, "test-circuit-2")
	require.Equal(t, int64(0), metrics["test-circuit-2"].TotalRequests)
}
