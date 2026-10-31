package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// node is a JSON value that remembers object key order, so tables keep the server's column order.
type node struct {
	kind   byte // 'o' object, 'a' array, 's' scalar
	keys   []string
	fields map[string]*node
	items  []*node
	scalar any // string, json.Number, bool or nil
}

func parseNode(data []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return decodeNode(dec)
}

func decodeNode(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		return decodeObject(dec)
	case json.Delim('['):
		return decodeArray(dec)
	}
	return &node{kind: 's', scalar: tok}, nil
}

func decodeObject(dec *json.Decoder) (*node, error) {
	n := &node{kind: 'o', fields: map[string]*node{}}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := kt.(string)
		v, err := decodeNode(dec)
		if err != nil {
			return nil, err
		}
		if _, dup := n.fields[k]; !dup {
			n.keys = append(n.keys, k)
		}
		n.fields[k] = v
	}
	_, err := dec.Token() // closing }
	return n, err
}

func decodeArray(dec *json.Decoder) (*node, error) {
	n := &node{kind: 'a'}
	for dec.More() {
		v, err := decodeNode(dec)
		if err != nil {
			return nil, err
		}
		n.items = append(n.items, v)
	}
	_, err := dec.Token() // closing ]
	return n, err
}

func (n *node) objectArray() bool {
	if n.kind != 'a' || len(n.items) == 0 {
		return false
	}
	for _, it := range n.items {
		if it.kind != 'o' {
			return false
		}
	}
	return true
}

// lookup walks a dot-separated path; numeric segments index arrays.
func (n *node) lookup(path string) (*node, error) {
	cur := n
	for _, seg := range strings.Split(path, ".") {
		switch cur.kind {
		case 'o':
			next, ok := cur.fields[seg]
			if !ok {
				return nil, fmt.Errorf("field %q not found (available: %s)", seg, strings.Join(cur.keys, ", "))
			}
			cur = next
		case 'a':
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(cur.items) {
				return nil, fmt.Errorf("index %q out of range (array has %d items)", seg, len(cur.items))
			}
			cur = cur.items[i]
		default:
			return nil, fmt.Errorf("cannot descend into scalar at %q", seg)
		}
	}
	return cur, nil
}

func (n *node) marshal() []byte {
	var b bytes.Buffer
	n.write(&b)
	return b.Bytes()
}

func (n *node) write(b *bytes.Buffer) {
	switch n.kind {
	case 'o':
		b.WriteByte('{')
		for i, k := range n.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			b.Write(kb)
			b.WriteByte(':')
			n.fields[k].write(b)
		}
		b.WriteByte('}')
	case 'a':
		b.WriteByte('[')
		for i, it := range n.items {
			if i > 0 {
				b.WriteByte(',')
			}
			it.write(b)
		}
		b.WriteByte(']')
	default:
		vb, _ := json.Marshal(n.scalar)
		b.Write(vb)
	}
}

// print writes an API response in the active format.
func (c *Ctx) print(raw json.RawMessage) error {
	if c.G.Quiet {
		return nil
	}
	if c.G.Field != "" {
		root, err := parseNode(raw)
		if err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		n, err := root.lookup(c.G.Field)
		if err != nil {
			return &notFoundError{err.Error()}
		}
		if n.kind == 's' {
			_, err := fmt.Fprintln(c.Stdout, scalarString(n.scalar, false))
			return err
		}
		if c.format == "table" {
			return renderTable(c.Stdout, n, nil)
		}
		raw = n.marshal()
	}
	if c.format == "table" {
		n, err := parseNode(raw)
		if err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		return renderTable(c.Stdout, n, c.cmd.Columns)
	}
	return writeJSON(c.Stdout, raw)
}

func writeJSON(w io.Writer, raw []byte) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return fmt.Errorf("server returned invalid JSON: %w", err)
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// printValue marshals a locally built value (not an API response) in the active format.
func (c *Ctx) printValue(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.print(b)
}

func (c *Ctx) printPlanned() error {
	if len(c.planned) == 0 {
		return errors.New("dry run produced no requests")
	}
	return c.printValue(map[string]any{keyDryRun: true, "requests": c.planned})
}

// renderTable prints n for humans. An array of objects (at the top, or as the one array-of-objects
// field of the response) becomes a table; everything else becomes "key  value" lines.
func renderTable(w io.Writer, n *node, cols []string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if n.kind == 'o' {
		delete(n.fields, "took")
		n.keys = without(n.keys, "took")
	}
	switch {
	case n.objectArray():
		writeRows(tw, n.items, cols)
	case n.kind == 'a' && len(n.items) == 0:
		_, _ = fmt.Fprintln(tw, "(none)")
	case n.kind == 'o':
		if key := primaryArray(n); key != "" {
			writeRows(tw, n.fields[key].items, cols)
			rest := &node{kind: 'o', keys: without(n.keys, key), fields: n.fields}
			if len(rest.keys) > 0 {
				_, _ = fmt.Fprintln(tw)
				writeKV(tw, "", rest)
			}
		} else {
			writeKV(tw, "", n)
		}
	default:
		writeKV(tw, "", n)
	}
	return tw.Flush()
}

// primaryArray returns the single field holding an array of objects, or an empty array when that
// is the only array, so `{"domains": []}` still reads as an empty table.
func primaryArray(n *node) string {
	var found []string
	for _, k := range n.keys {
		f := n.fields[k]
		if f.objectArray() || (f.kind == 'a' && len(f.items) == 0) {
			found = append(found, k)
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

func without(keys []string, drop string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != drop {
			out = append(out, k)
		}
	}
	return out
}

func writeRows(w io.Writer, rows []*node, cols []string) {
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(w, "(none)")
		return
	}
	if len(cols) == 0 {
		seen := map[string]bool{}
		for _, r := range rows {
			for _, k := range r.keys {
				if !seen[k] {
					seen[k] = true
					cols = append(cols, k)
				}
			}
		}
	}
	header := make([]string, len(cols))
	for i, col := range cols {
		header[i] = strings.ToUpper(col)
	}
	_, _ = fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, r := range rows {
		cells := make([]string, len(cols))
		for i, col := range cols {
			cells[i] = cell(col, r, col)
		}
		_, _ = fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
}

// cell renders the value at a (possibly dotted) column path for one row.
func cell(name string, row *node, path string) string {
	v, err := row.lookup(path)
	if err != nil {
		return ""
	}
	switch v.kind {
	case 's':
		return scalarString(v.scalar, isTimeKey(name))
	case 'a':
		parts := make([]string, 0, len(v.items))
		for _, it := range v.items {
			if it.kind != 's' {
				return truncate(string(v.marshal()), 60)
			}
			parts = append(parts, scalarString(it.scalar, false))
		}
		return strings.Join(parts, ",")
	default:
		return truncate(string(v.marshal()), 60)
	}
}

func writeKV(w io.Writer, prefix string, n *node) {
	switch n.kind {
	case 'o':
		if len(n.keys) == 0 && prefix != "" {
			_, _ = fmt.Fprintf(w, "%s\t{}\n", prefix)
		}
		for _, k := range n.keys {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			writeKV(w, p, n.fields[k])
		}
	case 'a':
		writeKVArray(w, prefix, n)
	default:
		_, _ = fmt.Fprintf(w, "%s\t%s\n", label(prefix), scalarString(n.scalar, isTimeKey(prefix)))
	}
}

// writeKVArray prints an array of scalars on one line and anything else one element per path.
func writeKVArray(w io.Writer, prefix string, n *node) {
	parts := make([]string, 0, len(n.items))
	for _, it := range n.items {
		if it.kind != 's' {
			for i, it := range n.items {
				writeKV(w, fmt.Sprintf("%s.%d", prefix, i), it)
			}
			return
		}
		parts = append(parts, scalarString(it.scalar, false))
	}
	_, _ = fmt.Fprintf(w, "%s\t%s\n", label(prefix), strings.Join(parts, ", "))
}

func label(p string) string {
	if p == "" {
		return "value"
	}
	return p
}

func scalarString(v any, asTime bool) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		if asTime {
			if f, err := t.Float64(); err == nil && f > 1e9 && f < 1e10 {
				return time.Unix(int64(f), 0).Local().Format("2006-01-02 15:04:05")
			}
		}
		return t.String()
	}
	return fmt.Sprint(v)
}

var timeKeyHints = []string{"date", "time", "_at", "seen", "until", "expires", "login", "last_active", "modified"}

func isTimeKey(path string) bool {
	k := path
	if i := strings.LastIndex(k, "."); i >= 0 {
		k = k[i+1:]
	}
	k = strings.ToLower(k)
	if k == "took" || k == "timer" || k == "validity" || strings.Contains(k, "count") {
		return false
	}
	for _, h := range timeKeyHints {
		if strings.Contains(k, h) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
