package main

import (
    "bytes"
    "os"
    "path/filepath"
    "testing"
)

func TestFormatFilePreservesRawString(t *testing.T) {
    path := filepath.Join(t.TempDir(), "sample.go")
    source := []byte("package sample\n\nconst value = `line one\n\tline two`\n\nfunc get() string {\n\treturn value\n}\n")
    if err := os.WriteFile(path, source, 0o600); err != nil {
        t.Fatal(err)
    }
    if err := formatFile(path, false); err == nil {
        t.Fatal("format check accepted tab indentation")
    }
    if err := formatFile(path, true); err != nil {
        t.Fatal(err)
    }
    formatted, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    if !bytes.Contains(formatted, []byte("\n    return value\n")) {
        t.Fatalf("wrong indentation in %q", formatted)
    }
    if !bytes.Contains(formatted, []byte("`line one\n\tline two`")) {
        t.Fatalf("raw string changed in %q", formatted)
    }
    if err := formatFile(path, false); err != nil {
        t.Fatal(err)
    }
}

func TestFormatFileRejectsInvalidSyntax(t *testing.T) {
    path := filepath.Join(t.TempDir(), "broken.go")
    source := []byte("package sample\nfunc {\n")
    if err := os.WriteFile(path, source, 0o600); err != nil {
        t.Fatal(err)
    }
    if err := formatFile(path, true); err == nil {
        t.Fatal("formatter accepted invalid Go source")
    }
    contents, err := os.ReadFile(path)
    if err != nil {
        t.Fatal(err)
    }
    if !bytes.Equal(contents, source) {
        t.Fatalf("invalid source was rewritten as %q", contents)
    }
}
