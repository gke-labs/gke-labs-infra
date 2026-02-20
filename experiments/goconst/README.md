# goconst

An experiment to add support for "constant" objects in Go using weak references and deep hashing.

## Usage

```go
import "github.com/gke-labs/gke-labs-infra/experiments/goconst"

type MyConfig struct {
    Port int
    Host string
}

func main() {
    cfg := &MyConfig{Port: 8080, Host: "localhost"}
    
    // Mark the object as constant.
    // Future mutations will be detected by the background poller or manual Check() calls.
    goconst.MarkConst(cfg)
    
    cfg.Port = 8081 // This mutation will be detected
}
```

Alternatively, use the type-safe wrapper:

```go
func main() {
    cfg := &MyConfig{Port: 8080, Host: "localhost"}
    c := goconst.WrapConst(cfg)

    fmt.Println(c.Read().Port)
}
```

## How it works

1. `goconst.MarkConst(ptr)` takes a pointer to an object and stores a weak reference to it along with a JSON-encoded hash of its current state.
2. `goconst.WrapConst(ptr)` is a convenience wrapper that calls `MarkConst` and returns a `Const[T]` value.
3. A background goroutine polls all tracked objects every minute.
3. If an object has been mutated (i.e., its current JSON hash differs from the original), the program panics.
4. Because we use weak references (`weak.Pointer`), `goconst` does not prevent tracked objects from being garbage collected. Once an object is collected, it is automatically removed from the tracking list.

## Testing

Run the tests with:

```bash
go test -v ./experiments/goconst/...
```
