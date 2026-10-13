package main

import (
	"context"
	"fmt"
	"os"

	"github.com/CST-Cat/NodeDance/internal/core/cli"
	"github.com/CST-Cat/NodeDance/internal/update"
)

var version = "dev"

func main() {
	if handled, err := update.Internal(os.Args[1:], os.Stdout, os.Stderr); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := cli.Run(context.Background(), os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr, version); err != nil {
		fmt.Fprintln(os.Stderr, "nodedance:", err)
		os.Exit(1)
	}
}
