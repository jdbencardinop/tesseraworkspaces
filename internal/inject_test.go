package internal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInjectGitPathPreservesMeaningfulWhitespace(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "trailing spaces", data: "/tmp/review/entry  \n", want: "/tmp/review/entry  "},
		{name: "tab", data: "/tmp/review/\tentry\n", want: "/tmp/review/\tentry"},
		{name: "internal newline", data: "/tmp/review/line\nentry\n", want: "/tmp/review/line\nentry"},
		{name: "trailing carriage return", data: "/tmp/review/entry \r\n", want: "/tmp/review/entry \r"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := injectGitPath([]byte(tc.data))
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInjectGitPathFileAcceptsCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gitdir")
	if err := os.WriteFile(path, []byte("/tmp/review/entry \r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readInjectGitPathFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/review/entry " {
		t.Fatalf("Git control file path = %q, want CRLF terminator removed", got)
	}
}

func TestGitPathOutputSeparatesStderr(t *testing.T) {
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	script := "#!/bin/sh\nprintf '/tmp/review/entry  \\n'\nprintf 'warning: diagnostic only\\n' >&2\n"
	if err := os.WriteFile(gitPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := gitPathOutput("/ignored", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/review/entry  " {
		t.Fatalf("path = %q, want trailing spaces preserved and stderr excluded", got)
	}
}
