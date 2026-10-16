package i18n

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// ErrPlaceholders is returned (wrapped) when a translation does not keep
// its source's placeholders.
var ErrPlaceholders = errors.New("placeholders not preserved")

// Placeholders parses an ICU-style message and returns the distinct
// argument signatures, sorted:"{n}" for a simple argument, "{n,number}" for a
// typed one, "{count,plural}" for plural / selectordinal blocks and
// "{g,select:female|male|other}" for select blocks (select keys are
// identifiers that must survive; plural categories legitimately differ per
// language, so only the block itself is recorded). Arguments nested inside
// plural/select branches are included. Text inside a branch is ordinary
// translatable text. It returns an error for unbalanced braces or a
// malformed argument.
func Placeholders(msg string) ([]string, error) {
	p := &icuParser{s: []rune(msg)}
	if err := p.message(false); err != nil {
		return nil, err
	}
	if p.i < len(p.s) {
		return nil, fmt.Errorf("unexpected %q at %d", p.s[p.i], p.i)
	}
	slices.Sort(p.out)
	// A set, not a multiset: plural branches differ per language (ja has
	// only "other"), so an argument nested in them repeats a different
	// number of times.
	return slices.Compact(p.out), nil
}

// CheckPlaceholders reports whether translated keeps exactly the arguments
// of source (see Placeholders). A source that does not parse as a message
// (a stray brace in a title) is not checked beyond translated being
// non-empty.
func CheckPlaceholders(source, translated string) error {
	want, err := Placeholders(source)
	if err != nil {
		return nil
	}
	got, err := Placeholders(translated)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrPlaceholders, err)
	}
	if !slices.Equal(want, got) {
		return fmt.Errorf("%w: want %s, got %s", ErrPlaceholders, strings.Join(want, " "), strings.Join(got, " "))
	}
	return nil
}

type icuParser struct {
	s   []rune
	i   int
	out []string
}

// message reads text and arguments up to the end, or up to (not past) the
// closing brace of a branch when nested.
func (p *icuParser) message(nested bool) error {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case '{':
			if err := p.argument(); err != nil {
				return err
			}
		case '}':
			if nested {
				return nil
			}
			return fmt.Errorf("unbalanced } at %d", p.i)
		default:
			p.i++
		}
	}
	if nested {
		return errors.New("unterminated branch")
	}
	return nil
}

func (p *icuParser) space() {
	for p.i < len(p.s) && unicode.IsSpace(p.s[p.i]) {
		p.i++
	}
}

func (p *icuParser) ident() string {
	start := p.i
	for p.i < len(p.s) {
		r := p.s[p.i]
		if r == '_' || r == '-' || r == '=' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			p.i++
			continue
		}
		break
	}
	return string(p.s[start:p.i])
}

// argument parses {name}, {name, type[, style]} or a plural/select block.
func (p *icuParser) argument() error {
	open := p.i
	p.i++ // {
	p.space()
	name := p.ident()
	if name == "" {
		return fmt.Errorf("empty argument at %d", open)
	}
	p.space()
	if p.i >= len(p.s) {
		return errors.New("unterminated argument")
	}
	if p.s[p.i] == '}' {
		p.i++
		p.out = append(p.out, "{"+name+"}")
		return nil
	}
	if p.s[p.i] != ',' {
		return fmt.Errorf("malformed argument at %d", open)
	}
	p.i++
	p.space()
	typ := p.ident()
	if typ == "" {
		return fmt.Errorf("missing argument type at %d", open)
	}
	p.space()
	if p.i >= len(p.s) {
		return errors.New("unterminated argument")
	}
	switch typ {
	case "plural", "selectordinal", "select":
		if p.s[p.i] != ',' {
			return fmt.Errorf("malformed %s at %d", typ, open)
		}
		p.i++
		var keys []string
		for {
			p.space()
			if p.i >= len(p.s) {
				return errors.New("unterminated " + typ)
			}
			if p.s[p.i] == '}' {
				p.i++
				break
			}
			key := p.ident()
			if key == "" {
				return fmt.Errorf("malformed %s selector at %d", typ, p.i)
			}
			p.space()
			if strings.HasPrefix(key, "offset") && p.i < len(p.s) && p.s[p.i] == ':' {
				p.i++
				p.space()
				p.ident()
				continue
			}
			if p.i >= len(p.s) || p.s[p.i] != '{' {
				return fmt.Errorf("selector %q without a branch", key)
			}
			p.i++
			if err := p.message(true); err != nil {
				return err
			}
			p.i++ // }
			keys = append(keys, key)
		}
		if len(keys) == 0 {
			return fmt.Errorf("%s without branches", typ)
		}
		if typ == "select" {
			slices.Sort(keys)
			p.out = append(p.out, "{"+name+",select:"+strings.Join(keys, "|")+"}")
		} else {
			p.out = append(p.out, "{"+name+",plural}")
		}
		return nil
	default:
		// {n, number} / {d, date, short}: the style runs to the closing brace.
		for p.i < len(p.s) && p.s[p.i] != '}' {
			if p.s[p.i] == '{' {
				return fmt.Errorf("unexpected { in %s style", typ)
			}
			p.i++
		}
		if p.i >= len(p.s) {
			return errors.New("unterminated argument")
		}
		p.i++
		p.out = append(p.out, "{"+name+","+typ+"}")
		return nil
	}
}
