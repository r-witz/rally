package spec

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ecmaRegexp is the reference engine for "pattern" and the "regex" format:
// JSON Schema patterns are ECMA-262 regular expressions (JSON Schema Core
// 2020-12, section 6.4), which Go's regexp is not. The reference is V8,
// through Node (new RegExp(p, "u")); without Node, the documents that have
// patterns cannot be checked (errNoNode).
func ecmaRegexp(s string) (jsonschema.Regexp, error) {
	node := nodeEngine()
	if node == nil {
		return nil, errNoNode
	}
	if err := node.compile(s); err != nil {
		return nil, err
	}
	return nodeRe{node, s}, nil
}

var errNoNode = errors.New("node is not installed: patterns cannot be checked against V8")

const nodeScript = `
const rl = require("readline").createInterface({input: process.stdin});
const cache = new Map();
rl.on("line", l => {
  const q = JSON.parse(l);
  try {
    let re = cache.get(q.p);
    if (!re) { re = new RegExp(q.p, "u"); cache.set(q.p, re); }
    process.stdout.write(q.s === undefined ? "ok\n" : re.test(q.s) ? "1\n" : "0\n");
  } catch (e) {
    process.stdout.write("E" + JSON.stringify(String(e)) + "\n");
  }
});
`

type nodeRegexps struct {
	mu  sync.Mutex
	in  io.Writer
	out *bufio.Reader
}

var (
	nodeOnce sync.Once
	node     *nodeRegexps
)

func nodeEngine() *nodeRegexps {
	nodeOnce.Do(func() {
		path, err := exec.LookPath("node")
		if err != nil {
			return
		}
		cmd := exec.Command(path, "-e", nodeScript)
		in, err1 := cmd.StdinPipe()
		out, err2 := cmd.StdoutPipe()
		if err1 != nil || err2 != nil || cmd.Start() != nil {
			return
		}
		node = &nodeRegexps{in: in, out: bufio.NewReader(out)}
	})
	return node
}

func (n *nodeRegexps) ask(q map[string]string) string {
	b, _ := json.Marshal(q)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.in.Write(append(b, '\n'))
	line, _ := n.out.ReadString('\n')
	return strings.TrimSuffix(line, "\n")
}

func (n *nodeRegexps) compile(p string) error {
	if r := n.ask(map[string]string{"p": p}); r != "ok" {
		return errors.New(r)
	}
	return nil
}

type nodeRe struct {
	n *nodeRegexps
	p string
}

func (r nodeRe) String() string { return r.p }

func (r nodeRe) MatchString(s string) bool {
	return r.n.ask(map[string]string{"p": r.p, "s": s}) == "1"
}

var (
	oasOnce   sync.Once
	oasSchema *jsonschema.Schema
	oasErr    error
)

// oas31 returns the official OpenAPI 3.1 schema (with the base dialect, so
// that schemas inside the document are checked too).
func oas31(t *testing.T) *jsonschema.Schema {
	t.Helper()
	oasOnce.Do(func() {
		c := jsonschema.NewCompiler()
		for url, file := range map[string]string{
			"https://spec.openapis.org/oas/3.1/schema/2026-11-05":      "schema_2026-11-05.json",
			"https://spec.openapis.org/oas/3.1/schema-base/2026-11-05": "schema-base_2026-11-05.json",
			"https://spec.openapis.org/oas/3.1/dialect/2024-11-10":     "dialect_2024-11-10.json",
			"https://spec.openapis.org/oas/3.1/meta/2024-11-10":        "meta_2024-11-10.json",
		} {
			f, err := os.Open("testdata/oas/" + file)
			if err != nil {
				oasErr = err
				return
			}
			doc, err := jsonschema.UnmarshalJSON(f)
			f.Close()
			if err != nil {
				oasErr = err
				return
			}
			if err := c.AddResource(url, doc); err != nil {
				oasErr = err
				return
			}
		}
		oasSchema, oasErr = c.Compile("https://spec.openapis.org/oas/3.1/schema-base/2026-11-05")
	})
	if oasErr != nil {
		t.Fatal(oasErr)
	}
	return oasSchema
}

// documented compiles the schema the OpenAPI document gives at pointer
// (e.g. a request body), with format assertions on, as rally asserts the
// formats of time.Time and uuid.UUID.
type documented struct {
	doc any
}

func parseDocument(t *testing.T, raw []byte) documented {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("document is not JSON: %v", err)
	}
	if err := oas31(t).Validate(doc); err != nil {
		t.Errorf("document is not valid OpenAPI 3.1: %v", err)
	}
	return documented{doc}
}

func (d documented) schema(pointer ...string) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.UseRegexpEngine(ecmaRegexp)
	c.AssertFormat()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("mem://doc.json", d.doc); err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, p := range pointer {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(p))
	}
	return c.Compile("mem://doc.json#" + b.String())
}

func validates(s *jsonschema.Schema, instance []byte) bool {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(instance))
	if err != nil {
		return false
	}
	return s.Validate(v) == nil
}
