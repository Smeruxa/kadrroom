package catalog

import "testing"

func TestDecodeCatalog(t *testing.T) {
    decoded, err := decode([]byte(`{"items":[]}`))
    if err != nil {
        t.Fatal(err)
    }
    if decoded.Items == nil || len(decoded.Items) != 0 {
        t.Fatalf("decoded items = %#v, want an empty slice", decoded.Items)
    }
}

func TestDecodeInvalidCatalog(t *testing.T) {
    for _, input := range []string{`{`, `{"items":false}`} {
        if _, err := decode([]byte(input)); err == nil {
            t.Fatalf("decode(%q) succeeded", input)
        }
    }
}
