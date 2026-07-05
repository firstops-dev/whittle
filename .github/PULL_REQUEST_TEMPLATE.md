## What & why

<!-- what changes, and the motivation -->

## Guarantee checklist

- [ ] `make test` and `go run ./bench` pass locally
- [ ] New compression behavior is pinned by a test (losslessness = reconstruction; routing = corpus case)
- [ ] New error paths fail open (original returned, never a broken tool call)
- [ ] GUARANTEES.md still holds (or is updated with the pinning test)
