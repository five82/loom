package main

import (
	"os"
	"testing"
)

func TestMainEntrypointReturnsAfterSuccessfulCommand(t *testing.T) {
	_, cfg := testCLI(t)
	previous := os.Args
	os.Args = []string{"loom", "--config", cfg, "status"}
	t.Cleanup(func() { os.Args = previous })
	main()
}
