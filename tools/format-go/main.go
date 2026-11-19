package main

import (
    "bytes"
    "errors"
    "flag"
    "fmt"
    "go/ast"
    "go/parser"
    "go/printer"
    "go/token"
    "io/fs"
    "log/slog"
    "os"
    "path/filepath"
    "strings"
)

func main() {
    write := flag.Bool("write", false, "write formatted Go source")
    flag.Parse()

    if err := formatTree(*write); err != nil {
        slog.Error("format Go source", "error", err)
        os.Exit(1)
    }
}

func formatTree(write bool) error {
    var issues []error
    err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
        if err != nil {
            return err
        }
        if entry.IsDir() {
            switch entry.Name() {
            case "node_modules", "vendor", "dist", "secrets":
                return filepath.SkipDir
            }
            if path == "skills" || (path != "." && strings.HasPrefix(entry.Name(), ".")) {
                return filepath.SkipDir
            }
            return nil
        }
        if entry.Type().IsRegular() && filepath.Ext(path) == ".go" {
            if err := formatFile(path, write); err != nil {
                issues = append(issues, fmt.Errorf("%s: %w", path, err))
            }
        }
        return nil
    })
    return errors.Join(append(issues, err)...)
}

func formatFile(path string, write bool) error {
    source, err := os.ReadFile(path)
    if err != nil {
        return err
    }
    fileSet := token.NewFileSet()
    file, err := parser.ParseFile(fileSet, path, source, parser.ParseComments|parser.SkipObjectResolution)
    if err != nil {
        return err
    }
    ast.SortImports(fileSet, file)

    var output bytes.Buffer
    config := printer.Config{Mode: printer.UseSpaces, Tabwidth: 4}
    if err := config.Fprint(&output, fileSet, file); err != nil {
        return err
    }
    if bytes.Equal(source, output.Bytes()) {
        return nil
    }
    if !write {
        return errors.New("needs formatting")
    }
    return os.WriteFile(path, output.Bytes(), 0o644)
}
