# Agent Sandboxes

This package provides a Go client and CLI tools for managing agent sandboxes.
Currently, it uses Kubernetes Pods to provide isolated environments for running tasks.

## Components

- **Go Client**: A fluent interface for programmatic management of sandboxes.
- **CLI Tool (`agentsandboxes`)**: A command-line interface for manual management.
- **MCP Server (`agentsandboxes-mcp`)**: A Model Context Protocol server to expose sandbox tools to AI agents.

## Usage

```go
import "github.com/gke-labs/gke-labs-infra/agentsandboxes"

// Create a new sandbox
sandbox, err := agentsandboxes.New("my-sandbox").
    WithImage("local/ap-golang:latest").
    Create(ctx)

// List sandboxes
sandboxes, err := agentsandboxes.List(ctx)
```
