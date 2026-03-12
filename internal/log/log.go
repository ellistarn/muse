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
