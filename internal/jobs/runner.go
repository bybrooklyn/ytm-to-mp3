package jobs

import "context"

// Runner starts a download run: a list of inputs (links of any mix, paths to exports or
// lists, pasted blocks; see package inputs) and a context that stops it. Everything that
// happens is reported to sink; Run returns when the run is over (finished, failed as a
// whole, or stopped by ctx). Songs that already arrived stay; running again continues.
//
// The terminal program implements this around its download function; a desktop window uses
// the same interface and shows the events it receives. Options (output folder, format,
// sources) belong to the implementation, set when it is built, so this interface stays
// small and stable.
type Runner interface {
	Run(ctx context.Context, inputs []string, sink Sink) error
}

// RunnerFunc adapts a function to a Runner.
type RunnerFunc func(ctx context.Context, inputs []string, sink Sink) error

func (f RunnerFunc) Run(ctx context.Context, inputs []string, sink Sink) error {
	return f(ctx, inputs, sink)
}
