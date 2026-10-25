package tools

// All returns every tool the server exposes, in a slice owned by the caller:
// changing it does not affect later calls.
func All() []Tool {
	a, b := GroupA(), GroupB()
	out := make([]Tool, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}
