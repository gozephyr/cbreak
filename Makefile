# Makefile for Golang package

# Variables
BINARY_NAME=cbreak
VERSION?=0.1.0
GOOS?=$(shell go env GOOS)

# Go commands
GO=go
GOTEST=$(GO) test
GOMOD=$(GO) mod
GOGET=$(GO) get
GOVET=$(GO) vet
GOFMT=gofmt
GOLINT=golangci-lint


.PHONY: test coverage run lint fmt vet tidy vendor help benchmark benchmark-cpu benchmark-mem godoc godoc-server

# Default target
all: lint test

# Run tests
test:
	@echo "Running tests..."
	@$(GOTEST) -race -v -timeout=5m ./...

# Run tests with coverage
coverage:
	@echo "Running tests with coverage..."
	@$(GOTEST) -v -coverprofile=coverage.out ./...
	@$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated at coverage.html"

# Run benchmarks
benchmark:
	@echo "Running all benchmarks..."
	@$(GOTEST) -bench=. -benchmem ./...

# Run CPU benchmarks
benchmark-cpu:
	@echo "Running CPU benchmarks..."
	@$(GOTEST) -bench=. -benchmem -cpuprofile=cpu.out ./...
	@$(GO) tool pprof -top cpu.out

# Run memory benchmarks
benchmark-mem:
	@echo "Running memory benchmarks..."
	@$(GOTEST) -bench=. -benchmem -memprofile=mem.out ./...
	@$(GO) tool pprof -top mem.out

# Run linter
lint:
	@echo "Running linter..."
	@$(GOLINT) run ./...

# Format code
fmt:
	@echo "Formatting code..."
	@$(GOFMT) -s -w .

# Run go vet
vet:
	@echo "Running go vet..."
	@$(GOVET) ./...

# Update go.mod
tidy:
	@echo "Tidying modules..."
	@$(GOMOD) tidy

# Download dependencies to vendor directory
vendor:
	@echo "Vendoring dependencies..."
	@$(GOMOD) vendor

# Install development tools
install-tools:
	@echo "Installing development tools..."
	@curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell go env GOPATH)/bin
	@go install github.com/golang/mock/mockgen@latest
	@go install golang.org/x/tools/cmd/goimports@latest

# Generate GoDoc HTML documentation
godoc:
	@echo "Static HTML generation is no longer supported by godoc."
	@echo "Use 'make godoc-server' and browse http://localhost:6060 instead."

godoc-server:
	@echo "Starting local GoDoc server at http://localhost:6060 ..."
	@go install golang.org/x/tools/cmd/godoc@latest
	@godoc -http=:6060

# Display help information
help:
	@echo "Available targets:"
	@echo "  test         - Run tests"
	@echo "  coverage     - Run tests and generate coverage report"
	@echo "  benchmark    - Run all benchmarks"
	@echo "  benchmark-cpu - Run CPU benchmarks with profiling"
	@echo "  benchmark-mem - Run memory benchmarks with profiling"
	@echo "  lint         - Run linter"
	@echo "  fmt          - Format code"
	@echo "  vet          - Run go vet"
	@echo "  tidy         - Update go.mod"
	@echo "  vendor       - Download dependencies to vendor directory"
	@echo "  install-tools - Install development tools"
	@echo "  godoc        - (Info only) Static HTML generation is not supported; use godoc-server"
	@echo "  godoc-server - Run a local GoDoc server at http://localhost:6060"
	@echo "  help         - Display this help"