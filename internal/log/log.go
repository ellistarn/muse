// Package log provides timestamped logging to stderr for diagnostic and
// progress output. All operational messages should use this package.
// User-facing output (results, streaming responses) goes to stdout via fmt.
// Never write directly to os.Stderr outside this package.
package log

import (
	"fmt"
	"os"
	"time"
)

func Printf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "["+time.Now().Format("15:04:05")+"] "+format, args...)
}

func Println(args ...any) {
	fmt.Fprintln(os.Stderr, append([]any{"[" + time.Now().Format("15:04:05") + "]"}, args...)...)
}
