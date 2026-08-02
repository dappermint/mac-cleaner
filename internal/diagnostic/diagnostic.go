package diagnostic

import (
	"context"
	"fmt"
	"io"
	"sync"
)

type state struct {
	writer io.Writer
	mutex  sync.Mutex
}

type contextKey struct{}

func WithContext(ctx context.Context, writer io.Writer) context.Context {
	if writer == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, &state{writer: writer})
}

func Enabled(ctx context.Context) bool {
	_, ok := ctx.Value(contextKey{}).(*state)
	return ok
}

func Printf(ctx context.Context, format string, arguments ...any) {
	value, ok := ctx.Value(contextKey{}).(*state)
	if !ok || value == nil {
		return
	}
	value.mutex.Lock()
	defer value.mutex.Unlock()
	fmt.Fprintf(value.writer, "debug: "+format+"\n", arguments...)
}
