// kyokai-cli is the console `kyokai` in the app's bin folder: Kyokai's
// command line (doc/en-us/cli.md), the same as `kyokai-svc ctl`. The build
// names it kyokai (kyokai.exe), which is not put on PATH.
package main

import (
	"os"

	"github.com/YuanZhe-99/Kyokai/internal/ctl"
)

func main() {
	os.Exit(ctl.Main("kyokai", os.Args[1:]))
}
