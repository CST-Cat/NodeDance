package main

import (
	"context"
	"fmt"
	"os"

	"github.com/CST-Cat/NodeDance/internal/core/cli"
)

var version = "dev"

func main() {
	if err := cli.Run(context.Background(), os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr, version); err != nil {
		fmt.Fprintln(os.Stderr, "nodedance:", err)
		os.Exit(1)
	}
}
