package codegen

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/soroworks/sorovault/internal/model"
)

// Placeholder byte strings, hexadecimal, so an example is also a string the
// schema's patterns accept.
//
// The all-zero public key address is the one the package's own tests use, and
// it matches both the Address pattern and the MuxedAddress pattern, which also
// spells the plain-account alternative. The all-zero BytesN<N> is a valid value
// of any fixed length and says plainly that it is a placeholder.
const exampleAddress = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

// Example builds one function's example arguments as JSON.
//
// The document is an object whose keys are the argument names the interface
// declares, in the order they are declared, so it is exactly the document the
// function's JSON Schema validates: a client can take it, change one field and
// have a first request. Every value is deterministic — the same interface and
// function always produce the same bytes — so examples can be checked into a
// repository and diffed.
//
// Placeholder values follow the schema's own conventions, and in particular
// its treatment of integers: a u32 is a JSON number, and anything wider —
// u64, u128, u256, timepoints, durations and their signed relatives — is a
// decimal string, because a JSON number cannot hold it exactly.
//
// An example cannot be built for every type: a function whose arguments are
// genuinely self-referential (a struct whose field is that same struct, not
// through a Vec, an Option or a Map) has no finite example, and the error
// says which type it stopped at.
func Example(iface *model.Interface, fn model.Function, src Source) ([]byte, error) {
	if iface == nil {
		return nil, fmt.Errorf("codegen: no interface to generate an example from")
	}

	g := &exampleGen{iface: iface, seen: map[string]bool{}}

	fields := make([]named, 0, len(fn.Inputs))
	for _, in := range fn.Inputs {
		raw, err := g.raw(in.Type, "argument "+in.Name+" of "+fn.Name)
		if err != nil {
			return nil, err
		}
		fields = append(fields, named{name: in.Name, raw: raw})
	}

	document, err := marshalObject(fields)
	if err != nil {
		return nil, fmt.Errorf("codegen: encoding the arguments of %s: %w", fn.Name, err)
	}
	return document, nil
}

// Examples builds example arguments for every function in an interface, as a
// JSON object keyed by function name. A function that cannot have an example
// fails the whole document, because an example set with a hole in it is a
// lying example set.
func Examples(iface *model.Interface, src Source) ([]byte, error) {
	if iface == nil {
		return nil, fmt.Errorf("codegen: no interface to generate examples from")
	}

	fields := make([]named, 0, len(iface.Functions))
	for _, fn := range iface.Functions {
		raw, err := Example(iface, fn, src)
		if err != nil {
			return nil, err
		}
		fields = append(fields, named{name: fn.Name, raw: raw})
	}

	return marshalObject(fields)
}

// named is one key of a JSON object in the order it is written, so documents
// keep the order the interface declares rather than the alphabetical order
// marshalling a map into.
type named struct {
	name string
	raw  json.RawMessage
}

// marshalObject writes fields as a JSON object.
func marshalObject(fields []named) (json.RawMessage, error) {
	out := make([]byte, 0, 1024)
	out = append(out, '{')
	for i, f := range fields {
		if i > 0 {
			out = append(out, ',')
		}
		key, err := json.Marshal(f.name)
		if err != nil {
			return nil, err
		}
		out = append(out, key...)
		out = append(out, ':')
		out = append(out, f.raw...)
	}
	out = append(out, '}')
	return out, nil
}

// exampleGen writes interface types as placeholder JSON.
type exampleGen struct {
	iface *model.Interface
	// seen holds the user-defined types currently being expanded, so a
	// type that directly contains itself fails with a message instead of
	// recursing forever. A type reached through a Vec, an Option or a Map
	// does not recurse into: those containers are emitted empty, which is
	// both valid and where a real caller would start.
	seen map[string]bool
}

// literal is a JSON chunk for a placeholder value that carries no structure.
func literal(raw string, err error) (json.RawMessage, error) {
	return json.RawMessage(raw), err
}

// raw is the placeholder for one type, as JSON bytes. At names where the value
// sits in the document, so a failure points at the argument rather than the
// middle of a type.
func (g *exampleGen) raw(t model.Type, at string) (json.RawMessage, error) {
	switch t.Kind {
	case model.KindVal:
		// A Val is anything the host accepts; the schema says so too.
		return literal("42", nil)

	case model.KindBool:
		return literal("false", nil)

	case model.KindVoid:
		return literal("null", nil)

	case model.KindError:
		// An error value is its numeric code.
		return literal("0", nil)

	case model.KindU32, model.KindI32:
		// Both are JSON numbers.
		return literal("1", nil)

	case model.KindU64, model.KindU128, model.KindU256, model.KindTimepoint,
		model.KindDuration, model.KindI64, model.KindI128, model.KindI256:
		// Wider than 53 bits, so a decimal string, as the schema spells
		// it. The signed pattern accepts "1" as written.
		return literal(`"1"`, nil)

	case model.KindBytes:
		// One zero byte, hexadecimal: valid, and clearly not real data.
		return literal(`"00"`, nil)

	case model.KindBytesN:
		if t.N == 0 {
			// The schema of a zero-length fixed string matches only
			// the empty string.
			return literal(`""`, nil)
		}
		return literal(`"`+strings.Repeat("0", int(2*t.N))+`"`, nil)

	case model.KindString:
		return literal(`"example"`, nil)

	case model.KindSymbol:
		// Symbols are letters, digits and underscores, at most 32 of
		// them.
		return literal(`"name_0"`, nil)

	case model.KindAddress, model.KindMuxedAddress:
		// The schema allows a C, G or M address for a muxed address, so
		// a plain account address is a valid spelling of both.
		return literal(`"`+exampleAddress+`"`, nil)

	case model.KindOption:
		// An Option is either null or what it wraps, and null is the
		// valid spelling of "absent" a caller starts with.
		return literal("null", nil)

	case model.KindVec:
		// The empty vector is valid whatever the element type, and it is
		// also what stops a type nested inside its own element from
		// recursing forever.
		return literal("[]", nil)

	case model.KindMap:
		// The empty map is valid whatever the key and value types.
		return literal("{}", nil)

	case model.KindTuple:
		items := make([]string, 0, len(t.Elements))
		for _, element := range t.Elements {
			raw, err := g.raw(element, at)
			if err != nil {
				return nil, err
			}
			items = append(items, string(raw))
		}
		return literal("["+strings.Join(items, ",")+"]", nil)

	case model.KindResult:
		// The Ok arm is the value a caller starts with; the schema
		// offers the error arm too.
		return g.child(t.Ok, at)

	case model.KindUDT:
		if g.seen[t.Name] {
			return nil, fmt.Errorf(
				"codegen: no finite example for %s: the type %s contains itself", at, t.Name)
		}
		g.seen[t.Name] = true
		defer delete(g.seen, t.Name)
		return g.udtRaw(t.Name, at)

	default:
		// A kind the generator does not know. The schema for an unknown
		// kind accepts anything, so null is a valid placeholder, and it
		// is called out here rather than guessed at.
		return literal("null", nil)
	}
}

// child is a nested value, tolerating a missing arm the way the schema
// generator's child does.
func (g *exampleGen) child(t *model.Type, at string) (json.RawMessage, error) {
	if t == nil {
		// A missing arm is a schema that accepts anything.
		return literal("null", nil)
	}
	return g.raw(*t, at)
}

// udtRaw is the placeholder for one user-defined type.
func (g *exampleGen) udtRaw(name, at string) (json.RawMessage, error) {
	for _, s := range g.iface.Types.Structs {
		if s.Name != name {
			continue
		}
		if s.IsTuple {
			// A tuple struct is encoded positionally.
			items := make([]string, 0, len(s.Fields))
			for _, f := range s.Fields {
				raw, err := g.raw(f.Type, at)
				if err != nil {
					return nil, err
				}
				items = append(items, string(raw))
			}
			return literal("["+strings.Join(items, ",")+"]", nil)
		}
		fields := make([]named, 0, len(s.Fields))
		for _, f := range s.Fields {
			raw, err := g.raw(f.Type, at)
			if err != nil {
				return nil, err
			}
			fields = append(fields, named{name: f.Name, raw: raw})
		}
		return marshalObject(fields)
	}

	for _, u := range g.iface.Types.Unions {
		if u.Name != name {
			continue
		}
		if len(u.Cases) == 0 {
			return nil, fmt.Errorf(
				"codegen: no finite example for %s: the union %s has no cases", at, name)
		}
		c := u.Cases[0]
		// The SDK's tagged object, with the first case: one value of each
		// kind it declares. A case without values is spelled with a null
		// values field, which is what its schema requires.
		values := []byte("null")
		if len(c.Values) > 0 {
			items := make([]string, 0, len(c.Values))
			for _, v := range c.Values {
				raw, err := g.raw(v, at)
				if err != nil {
					return nil, err
				}
				items = append(items, string(raw))
			}
			values = []byte("[" + strings.Join(items, ",") + "]")
		}
		return literal(`{"tag":`+strconv.Quote(c.Name)+`,"values":`+string(values)+`}`, nil)
	}

	for _, e := range g.iface.Types.Enums {
		if e.Name != name {
			continue
		}
		return g.enumRaw(len(e.Cases) == 0, e.Cases, name, at)
	}
	for _, e := range g.iface.Types.ErrorEnums {
		if e.Name != name {
			continue
		}
		// An error enum used as a value is its numeric code, the way the
		// TypeScript generator writes it.
		return g.enumRaw(len(e.Cases) == 0, e.Cases, name, at)
	}

	// A type the interface names but does not define: the schema accepts
	// anything, so the placeholder is null.
	return literal("null", nil)
}

// enumRaw is an enum or an error enum's first case, written as the number the
// schema enumerates.
func (g *exampleGen) enumRaw(empty bool, cases []model.EnumCase, name, at string) (json.RawMessage, error) {
	if empty {
		return nil, fmt.Errorf(
			"codegen: no finite example for %s: the type %s has no cases", at, name)
	}
	return literal(strconv.FormatUint(uint64(cases[0].Value), 10), nil)
}
