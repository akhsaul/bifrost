package main

import (
	"flag"
	"fmt"
	"os"
)

// versionFlag is set when the user runs the binary with -version; main prints
// the embedded build version and exits without booting the server.
var versionFlag = flag.Bool("version", false, "Print the build version and exit")

// printVersionAndExit writes the ldflags-injected Version to stdout and
// terminates the process successfully.
func printVersionAndExit() {
	fmt.Println(Version)
	os.Exit(0)
}
