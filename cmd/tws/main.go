package main

import (
	"fmt"
	"os"

	"github.com/jdbencardinop/tesseraworkspaces/internal/cli"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printHelp()
		return
	}

	switch os.Args[1] {
	case "--version", "-v":
		fmt.Printf("tws %s\n", version)
	case "--help", "-h":
		printHelp()
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
	case "list", "ls":
		cli.List(os.Args[2:])
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Printf("tws %s — tesseraworkspaces\n\n", version)
	fmt.Println("Usage: tws <command> [args]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  add <feature>                        Create a feature workspace")
	fmt.Println("  new <feature> <branch> [--base <b>]  Create a worktree branch")
	fmt.Println("  open <feature> <branch> [--tmux]    Open worktree and run agent")
	fmt.Println("  sync <feature>                       Rebase worktrees in dependency order")
	fmt.Println("  stack <feature>                      Show branch dependency tree")
	fmt.Println("  delete <feature>                     Remove feature and worktrees")
	fmt.Println("  list, ls                             List features and branches")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --version, -v  Print version")
	fmt.Println("  --help, -h     Print this help")
}
