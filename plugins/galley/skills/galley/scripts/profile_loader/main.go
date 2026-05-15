package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/shinpr/galley/internal/profile"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: profile_loader ENVIRONMENT.yaml")
		os.Exit(2)
	}
	env, err := profile.LoadEnvironment(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	result := profile.ValidateEnvironment(env)
	if !result.Valid() {
		fmt.Fprintf(os.Stderr, "invalid environment profile %s: %s\n", os.Args[1], strings.Join(result.Errors, "; "))
		os.Exit(2)
	}
	payload := struct {
		DefaultCLI string `json:"default_cli,omitempty"`
	}{}
	if env.Executor != nil {
		payload.DefaultCLI = env.Executor.DefaultCLI
	}
	if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
