package main

import (
	"os"

	"github.com/michaelishri/gonner/cmd/gonner/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
