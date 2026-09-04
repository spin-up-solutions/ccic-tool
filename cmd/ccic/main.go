package main

import (
	"fmt"
	"os"

	"github.com/spin-up-solutions/ccic-tool/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "\033[31mccic: %v\033[0m\n", err)
		os.Exit(1)
	}
}
