package main

import (
	"os"
)

var helperPath, controller, nonce string
var subcommand = "renderer"

func main() {
	if helperPath == "" || controller == "" || nonce == "" {
		os.Exit(2)
	}
	arguments := append([]string{helperPath, subcommand, controller, nonce}, os.Args[1:]...)
	if err := replace(helperPath, arguments, os.Environ()); err != nil {
		os.Exit(3)
	}
}
