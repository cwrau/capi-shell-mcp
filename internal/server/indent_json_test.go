package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestIndentJSONMatchesTheStandardLibraryIndenter(t *testing.T) {
	for name, src := range map[string]string{
		"scalar":                 `1`,
		"string":                 `"a"`,
		"null":                   `null`,
		"empty containers":       `{"a":{},"b":[],"c":[[],{}]}`,
		"nested":                 `{"a":[1,2,{"b":[true,false,null]}],"c":"d"}`,
		"punctuation in strings": `{"k":"a,b:c{d}e[f]\"g\\","[":"]"}`,
		"unicode and escapes":    `["é","<","\n","\\\""]`,
		"top level array":        `[{"a":1},{"b":2}]`,
	} {
		t.Run(name, func(t *testing.T) {
			var want bytes.Buffer
			if err := json.Indent(&want, []byte(src), "", "  "); err != nil {
				t.Fatal(err)
			}
			got, err := indentJSON([]byte(src), 0, 1<<20)
			if err != nil {
				t.Fatalf("indentJSON: %v", err)
			}
			if string(got) != want.String() {
				t.Errorf("indentJSON =\n%s\nwant\n%s", got, want.String())
			}
		})
	}
}

func TestIndentJSONStartsAtTheGivenDepth(t *testing.T) {
	got, err := indentJSON([]byte(`{"a":[1]}`), 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	// Lines inside the value are indented as if it sat two levels down, the
	// way the result field sits inside the cluster object inside the array.
	want := "{\n      \"a\": [\n        1\n      ]\n    }"
	if string(got) != want {
		t.Errorf("indentJSON = %q, want %q", got, want)
	}
}

func TestIndentJSONRefusesAnOutputPastTheLimitInsteadOfBuildingIt(t *testing.T) {
	// Small compact, huge indented: the nesting multiplies every line.
	src := strings.Repeat("[1,", 3000) + "1" + strings.Repeat("]", 3000)
	if len(src) > 20000 {
		t.Fatalf("test input is not small: %d bytes", len(src))
	}
	out, err := indentJSON([]byte(src), 0, 64*1024)
	if err == nil {
		t.Fatalf("indentJSON produced %d bytes from %d, want an error past the limit", len(out), len(src))
	}
	if len(out) > 64*1024+1024 {
		t.Errorf("it built %d bytes before giving up, want it to stop near the limit", len(out))
	}
}
