// Package contract reads the one document a repository uses to declare what it
// builds, deploys and publishes.
//
// A repository writes that document in one of several spellings. hanzo.yml,
// hanzo.yaml and hanzo.json are three ways to write the SAME document — YAML 1.2
// is a superset of JSON, so one parser reads all three, against one schema.
// hanzo.js, hanzo.ts and hanzo.go are generators: they are run once, at the edge,
// and what they print is the document (eval.go says why).
//
// So reading a contract is always: resolve → evaluate if the spelling is code →
// canonical document → digest. Load is the reading half and is what every
// downstream reader gets; Eval is the only thing in the estate that runs a
// contract.
package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"
)

// Names are the spellings of the contract, in scan order: the data forms first,
// then the generators.
//
// The order is NOT a tie-break. Two contract files in one repository is refused
// (ErrMany), because two files each claiming to be the contract is genuinely
// ambiguous, and a reader that silently picks one ends up disagreeing with the
// reader that picks the other about what the repository declares. What the order
// fixes is which name a scan looks at first, and so the order an error lists them
// in; Names[0] is the spelling to write when creating one.
var Names = []string{"hanzo.yml", "hanzo.yaml", "hanzo.json", "hanzo.js", "hanzo.ts", "hanzo.go"}

// Max bounds a contract: the most that is read from a file, and the most a
// generator may print. A declaration is small, and the tree it arrives in is
// whatever somebody pushed.
const Max = 1 << 20

var (
	// ErrNone reports that a repository declares no contract, in any spelling.
	// Most repositories carry none, so this is an outcome rather than a failure: a
	// caller that can proceed without one tests for it instead of treating every
	// error alike.
	ErrNone = errors.New("contract: none")
	// ErrMany reports two or more spellings present at once; the error names them.
	ErrMany = errors.New("contract: more than one")
)

// Read answers one repository-root file's bytes. A file that is not there answers
// an error satisfying errors.Is(err, fs.ErrNotExist); anything else is a real
// failure and resolution stops on it, so an unreadable contract is never mistaken
// for an absent one.
type Read func(name string) ([]byte, error)

// Doc is a repository's contract in canonical form.
type Doc struct {
	// Name is the file the document was read from, for logs and errors.
	Name string
	// Data is the canonical form: JSON, names in sorted order, no insignificant
	// space. Every spelling of one contract produces the same bytes.
	Data []byte
}

// Digest is the sha256 of the canonical form, lowercase hex. It names the
// DOCUMENT and not the file, so a receipt can say what a repository declared
// without carrying the declaration, and two readers can show they read the same
// thing.
func (d Doc) Digest() string {
	sum := sha256.Sum256(d.Data)
	return hex.EncodeToString(sum[:])
}

// Into decodes the document into a typed view. The canonical form is JSON, so a
// reader names its fields with json tags — the same names the document spells.
func (d Doc) Into(v any) error { return json.Unmarshal(d.Data, v) }

// Load resolves a repository's contract and returns it in canonical form.
//
// It READS, and never runs anything. That is why this is the door every
// downstream reader gets and Eval is not: a generator spelling is refused here,
// by name, so a reader holding only Load cannot execute a repository's file
// however the repository spells it.
func Load(read Read) (Doc, error) {
	name, body, err := find(read)
	if err != nil {
		return Doc{}, err
	}
	if toolchain(name) != nil {
		return Doc{}, fmt.Errorf("%s: a generator is evaluated where the project's toolchain is; read the document it prints", name)
	}
	return Parse(name, body)
}

// Parse turns a document you already have and already named into canonical form.
// It is for a file whose location is fixed by something other than the contract —
// a workflow under .hanzo/workflows. A repository's OWN contract is found with
// Load, never by naming a file here.
func Parse(name string, body []byte) (Doc, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(body, &n); err != nil {
		return Doc{}, fmt.Errorf("%s: %w", name, err)
	}
	// A file with nothing in it — empty, only comments, or an explicit empty
	// document — is a contract that declares nothing, which is a different answer
	// from no contract at all. An empty one parses to no node; `---` and `null`
	// parse to a document with no value.
	var v any = map[string]any{}
	if n.Kind != 0 {
		w := walk{left: maxValues}
		got, err := w.value(&n)
		if err != nil {
			return Doc{}, fmt.Errorf("%s: %w", name, err)
		}
		if got != nil {
			v = got
		}
	}
	if _, ok := v.(map[string]any); !ok {
		return Doc{}, fmt.Errorf("%s: a contract declares names, got %T", name, v)
	}
	data, err := canon(v)
	if err != nil {
		return Doc{}, fmt.Errorf("%s: %w", name, err)
	}
	return Doc{Name: name, Data: data}, nil
}

// find scans Names once and returns the single contract present. Every name is
// probed even after one is found, because finding a second one is the point.
func find(read Read) (string, []byte, error) {
	var name string
	var body []byte
	var all []string
	for _, n := range Names {
		b, err := read(n)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return "", nil, fmt.Errorf("%s: %w", n, err)
		}
		all = append(all, n)
		if len(all) == 1 {
			name, body = n, b
		}
	}
	switch len(all) {
	case 0:
		return "", nil, ErrNone
	case 1:
		return name, body, nil
	default:
		return "", nil, fmt.Errorf("%w: %s", ErrMany, strings.Join(all, " "))
	}
}

// maxValues bounds one walk. Reading data is bounded work, but an anchor
// referenced from inside its own value is a cycle and an anchor referenced
// repeatedly over several levels expands exponentially — neither shows in the
// file's size, so what is counted is the values produced.
const maxValues = 1 << 16

type walk struct{ left int }

// value turns a parsed node into the JSON value model: map[string]any, []any,
// json.Number, string, bool, nil. Aliases are followed and `<<` merges applied, so
// the result is what the document MEANS rather than how its author spelled it.
func (w *walk) value(n *yaml.Node) (any, error) {
	if w.left--; w.left < 0 {
		return nil, errors.New("more values than one declaration can hold")
	}
	switch n.Kind {
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, fmt.Errorf("line %d: no anchor named %q", n.Line, n.Value)
		}
		return w.value(n.Alias)
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return map[string]any{}, nil
		}
		return w.value(n.Content[0])
	case yaml.MappingNode:
		return w.mapping(n)
	case yaml.SequenceNode:
		s := make([]any, len(n.Content))
		for i, c := range n.Content {
			v, err := w.value(c)
			if err != nil {
				return nil, err
			}
			s[i] = v
		}
		return s, nil
	case yaml.ScalarNode:
		return scalar(n)
	}
	return nil, fmt.Errorf("line %d: unreadable", n.Line)
}

// mapping builds one map. A key is a name, a name is declared once, and `<<` fills
// in only the names the map does not declare itself — the YAML merge rule.
func (w *walk) mapping(n *yaml.Node) (any, error) {
	m := make(map[string]any, len(n.Content)/2)
	var merges []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag == "!!merge" {
			merges = append(merges, v)
			continue
		}
		if k.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: a key is a name", k.Line)
		}
		if _, twice := m[k.Value]; twice {
			return nil, fmt.Errorf("line %d: %q is declared twice", k.Line, k.Value)
		}
		val, err := w.value(v)
		if err != nil {
			return nil, err
		}
		m[k.Value] = val
	}
	for _, from := range merges {
		v, err := w.value(from)
		if err != nil {
			return nil, err
		}
		fill(m, v)
	}
	return m, nil
}

// fill copies the names a merged value carries that the map does not already
// declare. A sequence of maps merges earliest-first, as YAML says.
func fill(m map[string]any, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if _, has := m[k]; !has {
				m[k] = val
			}
		}
	case []any:
		for _, e := range t {
			fill(m, e)
		}
	}
}

// scalar renders one scalar in the JSON value model.
//
// A number JSON spells the same way keeps its literal text, so `retries: 3` and
// `"retries": 3` are one value and a big integer keeps every digit; a YAML-only
// spelling (0o755, 1_000, .5) is resolved to the number it means. Everything that
// is not null, a bool or a number is the text the author wrote — a JSON document
// can only spell a date as a string, and the two spellings of one contract must
// not differ over that.
func scalar(n *yaml.Node) (any, error) {
	switch n.Tag {
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return nil, fmt.Errorf("line %d: %w", n.Line, err)
		}
		return b, nil
	case "!!int", "!!float":
		var num json.Number
		if json.Unmarshal([]byte(n.Value), &num) == nil {
			return num, nil
		}
		var f float64
		if n.Decode(&f) == nil {
			return json.Number(fmt.Sprintf("%v", f)), nil
		}
	}
	return n.Value, nil
}

// canon renders a value as the canonical form: JSON with names in sorted order.
// encoding/json sorts map keys, which is why the value model is a plain map and
// not an ordered one — a contract means the same thing whatever order its author
// typed the names in, and a digest that disagreed would differ between two
// spellings of one document.
func canon(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
