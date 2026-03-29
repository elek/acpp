package cli

import (
	"bufio"
	"fmt"
	"os"

	"github.com/alecthomas/kong"
)

type Read struct {
	Line  int    `help:"Line number to start reading from (1-based)" default:"1"`
	Limit int    `help:"Maximum number of lines to read (0 = unlimited)" default:"0"`
	Path  string `arg:"" help:"Absolute path to the file to read"`
}

func (r *Read) Run(kctx *kong.Context) error {
	f, err := os.Open(r.Path)
	if err != nil {
		return fmt.Errorf("open %s: %w", r.Path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer

	lineNum := 0
	printed := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < r.Line {
			continue
		}
		if r.Limit > 0 && printed >= r.Limit {
			break
		}
		fmt.Println(scanner.Text())
		printed++
	}
	return scanner.Err()
}
