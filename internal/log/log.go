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
	fmt.Fprint(os.Stderr, "["+time.Now().Format("15:04:05")+"] ")
	fmt.Fprintln(os.Stderr, args...)
}
