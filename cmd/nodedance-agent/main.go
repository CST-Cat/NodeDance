package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Printf("nodedance-agent %s (Agent enrollment and runtime: NOT_READY, S02)\n", version)
		return
	}
	if len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		fmt.Println("nodedance-agent version\nAgent enrollment and runtime will be implemented and tested in S02.")
		return
	}
	fmt.Fprintln(os.Stderr, "nodedance-agent: Agent enrollment and runtime are NOT_READY (S02)")
	os.Exit(2)
}
