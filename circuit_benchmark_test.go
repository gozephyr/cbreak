package cbreak

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// BenchmarkBreakerExecute benchmarks the basic Execute operation
func BenchmarkBreakerExecute(b *testing.B) {
	breaker, _ := NewBreaker[string](DefaultConfig("benchmark"))
	defer breaker.Shutdown()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = breaker.Execute(context.Background(), func() (string, error) {
				return "success", nil
			})
		}
	})
}

// BenchmarkBreakerExecuteError benchmarks Execute with errors
func BenchmarkBreakerExecuteError(b *testing.B) {
	breaker, _ := NewBreaker[string](DefaultConfig("benchmark"))
	defer breaker.Shutdown()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = breaker.Execute(context.Background(), func() (string, error) {
				return "", ErrTimeout
			})
		}
	})
}

// BenchmarkBreakerGetState benchmarks the GetState operation
func BenchmarkBreakerGetState(b *testing.B) {
	breaker, _ := NewBreaker[string](DefaultConfig("benchmark"))
	defer breaker.Shutdown()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			breaker.GetState()
		}
	})
}

// BenchmarkBreakerGetMetrics benchmarks the GetMetrics operation
func BenchmarkBreakerGetMetrics(b *testing.B) {
	breaker, _ := NewBreaker[string](DefaultConfig("benchmark"))
	defer breaker.Shutdown()

	// Execute some operations to populate metrics
	for i := 0; i < 100; i++ {
		_, _ = breaker.Execute(context.Background(), func() (string, error) {
			return "success", nil
		})
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			breaker.GetMetrics()
		}
	})
}

// BenchmarkBreakerStateTransitions benchmarks state transitions
func BenchmarkBreakerStateTransitions(b *testing.B) {
	config := DefaultConfig("benchmark")
	config.FailureThreshold = 5
	config.SuccessThreshold = 3
	config.Timeout = 100 * time.Millisecond

	breaker, _ := NewBreaker[string](config)
	defer breaker.Shutdown()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			// Execute operations that will trigger state transitions
			_, _ = breaker.Execute(context.Background(), func() (string, error) {
				return "", ErrTimeout
			})
		}
	})
}

// BenchmarkBreakerConcurrentMixed benchmarks mixed operations
func BenchmarkBreakerConcurrentMixed(b *testing.B) {
	breaker, _ := NewBreaker[string](DefaultConfig("benchmark"))
	defer breaker.Shutdown()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		counter := 0
		for pb.Next() {
			counter++
			if counter%3 == 0 {
				// Every 3rd operation is an error
				_, _ = breaker.Execute(context.Background(), func() (string, error) {
					return "", ErrTimeout
				})
			} else {
				_, _ = breaker.Execute(context.Background(), func() (string, error) {
					return "success", nil
				})
			}
		}
	})
}

// BenchmarkBreakerWithTimeout benchmarks operations with timeout
func BenchmarkBreakerWithTimeout(b *testing.B) {
	config := DefaultConfig("benchmark")
	config.CommandTimeout = 10 * time.Millisecond

	breaker, _ := NewBreaker[string](config)
	defer breaker.Shutdown()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = breaker.Execute(context.Background(), func() (string, error) {
				time.Sleep(5 * time.Millisecond)
				return "success", nil
			})
		}
	})
}

// BenchmarkBreakerHalfOpen benchmarks half-open state operations
func BenchmarkBreakerHalfOpen(b *testing.B) {
	config := DefaultConfig("benchmark")
	config.FailureThreshold = 1
	config.SuccessThreshold = 1
	config.Timeout = 1 * time.Millisecond
	config.HalfOpenMaxRequests = 10

	breaker, _ := NewBreaker[string](config)
	defer breaker.Shutdown()

	// Trigger open state
	_, _ = breaker.Execute(context.Background(), func() (string, error) {
		return "", ErrTimeout
	})

	// Wait for half-open
	time.Sleep(2 * time.Millisecond)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = breaker.Execute(context.Background(), func() (string, error) {
				return "success", nil
			})
		}
	})
}

// BenchmarkBreakerRegistry benchmarks registry operations
func BenchmarkBreakerRegistry(b *testing.B) {
	registry := NewRegistry[string]()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		counter := 0
		for pb.Next() {
			counter++
			name := fmt.Sprintf("breaker-%d", counter%10)
			breaker, _ := registry.GetOrCreate(name, DefaultConfig(name))
			_, _ = breaker.Execute(context.Background(), func() (string, error) {
				return "success", nil
			})
		}
	})
}

// BenchmarkBreakerMemoryAllocation benchmarks memory allocation
func BenchmarkBreakerMemoryAllocation(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		breaker, _ := NewBreaker[string](DefaultConfig("benchmark"))
		_, _ = breaker.Execute(context.Background(), func() (string, error) {
			return "success", nil
		})
		breaker.Shutdown()
	}
}
