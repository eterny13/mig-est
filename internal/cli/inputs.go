package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type sqlInput struct {
	Name string
	Data []byte
}

// gatherInputs resolves files, globs, directories, and stdin in a stable order.
func gatherInputs(files, dirs []string, pattern string, useStdin bool, stdin io.Reader) ([]sqlInput, error) {
	if pattern == "" {
		pattern = "*.sql"
	}
	var paths []string
	seen := make(map[string]bool)
	addPath := func(path string) error {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		if seen[abs] {
			return nil
		}
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return fmt.Errorf("%s is a directory; use --dir", path)
		}
		if fi.Size() > 32*1024*1024 {
			return fmt.Errorf("%s is larger than 32 MiB; split the migration file", path)
		}
		seen[abs] = true
		paths = append(paths, path)
		return nil
	}

	for _, file := range files {
		if hasGlobMeta(file) {
			matches, err := filepath.Glob(file)
			if err != nil {
				return nil, fmt.Errorf("invalid --file pattern %q: %w", file, err)
			}
			sort.Strings(matches)
			if len(matches) == 0 {
				return nil, fmt.Errorf("--file pattern %q matched no files", file)
			}
			for _, m := range matches {
				if err := addPath(m); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := addPath(file); err != nil {
			return nil, err
		}
	}

	for _, dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, fmt.Errorf("invalid --pattern %q: %w", pattern, err)
		}
		sort.Strings(matches)
		if len(matches) == 0 {
			return nil, fmt.Errorf("no files matched %s in %s", pattern, dir)
		}
		for _, m := range matches {
			if err := addPath(m); err != nil {
				return nil, err
			}
		}
	}

	var out []sqlInput
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", path, err)
		}
		if !utf8.Valid(data) {
			return nil, fmt.Errorf("%s is not valid UTF-8", path)
		}
		out = append(out, sqlInput{Name: displayPath(path), Data: data})
	}
	if useStdin {
		data, err := io.ReadAll(io.LimitReader(stdin, 32*1024*1024+1))
		if err != nil {
			return nil, fmt.Errorf("cannot read stdin: %w", err)
		}
		if len(data) > 32*1024*1024 {
			return nil, fmt.Errorf("stdin is larger than 32 MiB")
		}
		if !utf8.Valid(data) {
			return nil, fmt.Errorf("stdin is not valid UTF-8")
		}
		out = append(out, sqlInput{Name: "<stdin>", Data: data})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no SQL input; specify files or use --file, --dir, or --stdin")
	}
	return out, nil
}

func hasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func displayPath(path string) string {
	cwd, err := os.Getwd()
	if err == nil {
		if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}
