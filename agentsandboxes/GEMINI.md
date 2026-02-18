# Agent Sandboxes

This package is intended to be the central place for managing isolated execution environments (sandboxes).

## Future Work

- **CRD Support**: Transition from using raw Pods to using the `agent-sandbox` CRD from `https://github.com/kubernetes-sigs/agent-sandbox`.
- **MCP Implementation**: Fully implement the MCP server to allow agents to dynamically create and use sandboxes.
- **Improved Client**: Enhance the Go client with more features like port-forwarding, file synchronization (similar to what's in `ap/pkg/sandbox/sandbox.go`), and log streaming.
- **Testing**: Add unit and integration tests using a mock Kubernetes client or a test cluster.

## Dependencies

- Currently relies on `kubectl` being present in the PATH.
- Future versions should consider using `k8s.io/client-go` for a more robust implementation if it becomes a project standard.
