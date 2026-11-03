package main

import (
	"fmt"
	"os"

	"github.com/company/telegram-gateway/api/internal/password"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: hashpass <password>")
		os.Exit(2)
	}
	h, err := password.New().Hash(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "hash: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(h)
}
