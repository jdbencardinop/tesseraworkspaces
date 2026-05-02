package main

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal/cli"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: tws <command>")
		return
	}

	switch os.Args[1] {
	case "add":
		cli.Add(os.Args[2:])
	case "new":
		cli.New(os.Args[2:])
	case "open":
		cli.Open(os.Args[2:])
	case "sync":
		cli.Sync(os.Args[2:])
	case "stack":
		cli.StackCmd(os.Args[2:])
	case "delete":
		cli.Delete(os.Args[2:])
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		fmt.Println("Available commands: add, new, open, sync, stack, delete")
	}
}
