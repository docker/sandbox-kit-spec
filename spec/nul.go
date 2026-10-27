package spec

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// validateNoNUL refuses U+0000 in every string a descriptor holds: field
// values, map keys, and the config of every capability type, unknown
// types included. No field has a use for one. Paths, argv, and
// environment values cannot carry a NUL through the operating system,
// and a consumer that reads a string as C sees it end at the NUL, so the
// value enforced would not be the value validated.
//
// One walk instead of a rule per field, because a per-field rule is one
// the next field forgets. It runs inside Validate, so authoring,
// publication, and the effective gate after expansion all apply it; a
// literal NUL beside a reference is refused before expansion, which
// cannot remove it. Errors name the field and never print the value.
func validateNoNUL(d *Descriptor) error {
	w := nulWalk{active: map[nulVisit]bool{}}
	w.walk(reflect.ValueOf(d).Elem(), nil)
	return w.errs.err()
}

type nulWalk struct {
	errs ValidationErrors
	// active holds the maps, slices, and pointers on the current descent.
	// Decoding never produces a cycle, but a Go caller can build a Config
	// that contains itself, and following one would overflow the stack.
	active map[nulVisit]bool
}

// nulVisit names one container on the descent. Length is part of it, so
// a slice holding a shorter slice of its own array is not mistaken for
// one that holds itself.
type nulVisit struct {
	kind reflect.Kind
	ptr  uintptr
	len  int
}

// nulPath is a field path spelled only when an error reports it. Building
// every node's dotted string up front would cost the walk the document's
// depth at every node.
type nulPath struct {
	parent *nulPath
	key    string
	index  int // used when key is unset
	keyed  bool
}

func (p *nulPath) field(name string) *nulPath { return &nulPath{parent: p, key: name, keyed: true} }
func (p *nulPath) item(i int) *nulPath        { return &nulPath{parent: p, index: i} }

// String spells the path the way Positions keys a document.
func (p *nulPath) String() string {
	var steps []*nulPath
	for ; p != nil; p = p.parent {
		steps = append(steps, p)
	}
	var b strings.Builder
	for i := len(steps) - 1; i >= 0; i-- {
		switch s := steps[i]; {
		case !s.keyed:
			b.WriteString("[" + strconv.Itoa(s.index) + "]")
		case b.Len() == 0:
			b.WriteString(s.key)
		default:
			b.WriteString("." + s.key)
		}
	}
	return b.String()
}

func (w *nulWalk) walk(v reflect.Value, at *nulPath) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return
		}
		visit := nulVisit{kind: v.Kind(), ptr: v.Pointer(), len: -1}
		if v.Kind() == reflect.Slice {
			visit.len = v.Len()
		}
		if w.active[visit] {
			w.errs.add(fieldErrorf(at.String(), "holds a value that contains itself"))
			return
		}
		w.active[visit] = true
		defer delete(w.active, visit)
	}

	switch v.Kind() {
	case reflect.String:
		if strings.ContainsRune(v.String(), 0) {
			w.errs.add(fieldErrorf(at.String(), "contains NUL (U+0000), which no descriptor string may hold"))
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			w.walk(v.Elem(), at)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			w.walk(v.Index(i), at.item(i))
		}
	case reflect.Map:
		type entry struct {
			name  string
			value reflect.Value
		}
		entries := make([]entry, 0, v.Len())
		for iter := v.MapRange(); iter.Next(); {
			entries = append(entries, entry{fmt.Sprint(iter.Key().Interface()), iter.Value()})
		}
		slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.name, b.name) })
		for _, e := range entries {
			// Reported at the map, so the path in the error carries no NUL.
			if strings.ContainsRune(e.name, 0) {
				w.errs.add(fieldErrorf(at.String(), "a key contains NUL (U+0000), which no descriptor string may hold"))
				continue
			}
			w.walk(e.value, at.field(e.name))
		}
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if !f.IsExported() || tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if name == "" {
				name = f.Name
			}
			w.walk(v.Field(i), at.field(name))
		}
	}
}
