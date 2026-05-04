package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// pick presents a list of options to the user and returns the selected one.
// Uses fzf if available, falls back to a numbered list.
func pick(prompt string, options []string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("no options available")
	}
	if len(options) == 1 {
		return options[0], nil
	}

	if hasFzf() {
		return pickWithFzf(prompt, options)
	}
	return pickWithList(prompt, options)
}

func hasFzf() bool {
	_, err := exec.LookPath("fzf")
	return err == nil
}

func pickWithFzf(prompt string, options []string) (string, error) {
	cmd := exec.Command("fzf", "--prompt", prompt+" ", "--height", "~40%", "--reverse")
	cmd.Stdin = strings.NewReader(strings.Join(options, "\n"))
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("selection cancelled")
	}
	return strings.TrimSpace(string(out)), nil
}

func pickWithList(prompt string, options []string) (string, error) {
	fmt.Println(prompt)
	for i, opt := range options {
		fmt.Printf("  [%d] %s\n", i+1, opt)
	}
	fmt.Print("Select: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return "", fmt.Errorf("no input")
	}

	input := strings.TrimSpace(scanner.Text())

	// Try as number
	for i, opt := range options {
		if input == fmt.Sprintf("%d", i+1) {
			return opt, nil
		}
	}

	// Try as exact match
	for _, opt := range options {
		if opt == input {
			return opt, nil
		}
	}

	return "", fmt.Errorf("invalid selection: %s", input)
}
