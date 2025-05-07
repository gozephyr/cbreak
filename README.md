# Circuit Breaker Library

[![Go Report Card](https://goreportcard.com/badge/github.com/gozephyr/cbreak)](https://goreportcard.com/report/github.com/gozephyr/cbreak)
[![GoDoc](https://godoc.org/github.com/gozephyr/cbreak?status.svg)](https://godoc.org/github.com/gozephyr/cbreak)
[![Build Status](https://github.com/gozephyr/cbreak/actions/workflows/go.yml/badge.svg)](https://github.com/gozephyr/cbreak/actions/workflows/go.yml)
[![Coverage Status](https://codecov.io/gh/gozephyr/cbreak/branch/main/graph/badge.svg)](https://codecov.io/gh/gozephyr/cbreak)
[![Go Version](https://img.shields.io/badge/Go-1.21%2B-blue.svg)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A simple and efficient circuit breaker implementation in Go. This library helps prevent cascading failures in distributed systems by monitoring for failures and stopping the flow of requests when necessary.

## Documentation

Full documentation is available on [GoDoc](https://godoc.org/github.com/gozephyr/cbreak).

## Features

- Simple and intuitive API
- Configurable failure and success thresholds
- Automatic state transitions
- Built-in retry mechanism
- Fallback support
- Bulkhead pattern for concurrency control
- Metrics collection
- Structured logging
- Thread-safe operations
- Registry for managing multiple circuit breakers
- Support for different return types using generics

## Installation

```bash
go get github.com/gozephyr/cbreak
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "github.com/gozephyr/cbreak"
    "log"
    "time"
)

func main() {
    // Create a circuit breaker with custom configuration
    config := cbreak.DefaultConfig("my-service")
    config.FailureThreshold = 3
    config.Timeout = 5 * time.Second

    breaker, err := cbreak.NewBreaker[string](config)
    if err != nil {
        log.Fatal(err)
    }

    // Execute an operation
    result, err := breaker.Execute(context.Background(), func() (string, error) {
        // Your operation here
        return "success", nil
    })
    if err != nil {
        fmt.Printf("Error: %v\n", err)
        return
    }
    fmt.Printf("Result: %v\n", result)
}
```

## Configuration

The circuit breaker can be configured with various options:

### Basic Configuration

```go
config := cbreak.DefaultConfig("service")
config.FailureThreshold = 5        // Number of failures before opening circuit
config.SuccessThreshold = 3        // Number of successes before closing circuit
config.Timeout = 30 * time.Second  // Time to wait before attempting to close circuit
config.Logger = cbreak.NewDefaultLogger(cbreak.InfoLevel)
```

### Complete Configuration Options

```go
config := cbreak.DefaultConfig("service")
config.FailureThreshold = 5        // Number of failures before opening circuit
config.SuccessThreshold = 3        // Number of successes before closing circuit
config.Timeout = 30 * time.Second  // Time to wait before attempting to close circuit
config.MaxConcurrentCalls = 10     // Maximum number of concurrent calls allowed
config.Logger = cbreak.NewDefaultLogger(cbreak.InfoLevel)
config.MetricsEnabled = true       // Enable metrics collection
config.Name = "service-name"       // Name of the circuit breaker
```

### Retry Configuration

```go
retryConfig := cbreak.DefaultRetryConfig()
retryConfig.MaxAttempts = 3                    // Maximum number of retry attempts
retryConfig.InitialDelay = 100 * time.Millisecond  // Initial delay between retries
retryConfig.MaxDelay = 1 * time.Second         // Maximum delay between retries
retryConfig.Multiplier = 2.0                   // Multiplier for exponential backoff
retryConfig.RandomizationFactor = 0.1          // Randomization factor for jitter
```

## Advanced Usage

### Retry Mechanism

```go
retryConfig := cbreak.DefaultRetryConfig()
retryConfig.MaxAttempts = 3
retryConfig.InitialDelay = 100 * time.Millisecond

result, err := breaker.ExecuteWithRetry(context.Background(), func() (string, error) {
    // Your operation here
    return "", nil
}, retryConfig)
```

### Fallback Support

```go
result, err := breaker.ExecuteWithFallback(context.Background(),
    func() (string, error) {
        // Primary operation
        return "", fmt.Errorf("primary failed")
    },
    func(err error) (string, error) {
        // Fallback operation
        return "fallback", nil
    },
)
```

### Using the Registry

The registry provides a way to manage multiple circuit breakers:

```go
// Create a new registry
registry := cbreak.NewRegistry[string]()

// Get or create a circuit breaker
breaker := registry.GetOrCreate("service", cbreak.DefaultConfig("service"))

// Execute operations
result, err := breaker.Execute(context.Background(), func() (string, error) {
    return "success", nil
})

// Get metrics for all breakers
metrics := registry.GetMetrics()

// Get states of all breakers
states := registry.GetStates()

// Remove a breaker
registry.Remove("service")

// Reset all breakers
registry.ResetAll()
```

### Using Default Registry

```go
// Get a breaker from default registry
breaker, err := cbreak.Get[string]("service")

// Get a breaker with custom config
config := cbreak.DefaultConfig("service")
breaker, err = cbreak.GetWithConfig[string]("service", config)

// Get metrics from default registry
metrics := cbreak.GetMetrics[string]()

// Get states from default registry
states := cbreak.GetStates[string]()
```

## Metrics and Monitoring

The circuit breaker provides detailed metrics:

```go
metrics := breaker.GetMetrics()
fmt.Printf("Successful Calls: %d\n", metrics.SuccessfulCalls)
fmt.Printf("Failed Calls: %d\n", metrics.FailedCalls)
fmt.Printf("Rejected Calls: %d\n", metrics.RejectedCalls)
fmt.Printf("Total Requests: %d\n", metrics.TotalRequests)
fmt.Printf("Current State: %s\n", metrics.State)
```

## Circuit States

The circuit breaker operates in three states:

1. **Closed**: Normal operation, requests are allowed
2. **Open**: Circuit is open, requests are rejected
3. **Half-Open**: Testing if the service has recovered

```go
state := breaker.GetState()
switch state {
case cbreak.Closed:
    fmt.Println("Circuit is closed - normal operation")
case cbreak.Open:
    fmt.Println("Circuit is open - requests are rejected")
case cbreak.HalfOpen:
    fmt.Println("Circuit is half-open - testing recovery")
}
```

## Best Practices

1. Always close the circuit breaker when done:

   ```go
   defer breaker.Close()
   ```

2. Use appropriate timeouts in your context:

   ```go
   ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
   defer cancel()
   ```

3. Configure the circuit breaker based on your service's characteristics:
   - Set appropriate failure thresholds
   - Configure timeouts based on your service's response time  

4. Monitor the circuit breaker's state and metrics:

   ```go
   metrics := breaker.GetMetrics()
   state := breaker.GetState()
   ```

5. Use the registry for managing multiple circuit breakers:
   - Keep track of all breakers in one place
   - Monitor metrics across all breakers
   - Reset or remove breakers as needed

6. Implement proper error handling:
   - Use fallback mechanisms for critical operations
   - Implement retry logic for transient failures
   - Log errors appropriately

## Contributing

We welcome contributions! Please see our [Contributing Guidelines](CONTRIBUTING.md) for details.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## Versioning

We use [SemVer](http://semver.org/) for versioning. For the versions available, see the [CHANGELOG.md](CHANGELOG.md) file.

## Support

If you encounter any problems or have suggestions, please [open an issue](https://github.com/gozephyr/cbreak/issues).
