package rules

import (
	"fmt"
	"strings"
)

// Parse parses rules from data. The filename is used in error messages.
func Parse(filename string, data []byte) (*File, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	file := &File{}
	var current *Rule

	for i, line := range lines {
		lineno := i + 1

		// Directives must be checked before comment stripping.
		if d, ok := parseDirective(line); ok {
			switch d.Key {
			case "attach":
				return nil, fmt.Errorf("%s:%d: @attach is no longer supported; use \"fn=Type#Method\" instead", filename, lineno)
			case "detach":
				return nil, fmt.Errorf("%s:%d: @detach is no longer supported; use \"Type#Method=!name\" instead", filename, lineno)
			}
			file.Directives = append(file.Directives, d)
			current = nil
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "@") {
			return nil, fmt.Errorf("%s:%d: invalid directive", filename, lineno)
		}

		// Check indentation before stripping comments, so that
		// indented comment-only lines do not break multiline blocks.
		indented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')

		line = stripComment(line)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if !indented {
				current = nil
			}
			continue
		}
		if indented {
			if current == nil {
				return nil, fmt.Errorf("%s:%d: unexpected indented line", filename, lineno)
			}
			items, err := parseItems(trimmed)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", filename, lineno, err)
			}
			for _, it := range items {
				if it.IsFileMove {
					return nil, fmt.Errorf("%s:%d: file-move items cannot appear in multiline blocks", filename, lineno)
				}
			}
			current.Items = append(current.Items, items...)
			continue
		}

		dest, items, err := parseLine(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", filename, lineno, err)
		}
		if err := validateFileMove(items, dest); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", filename, lineno, err)
		}
		file.Rules = append(file.Rules, Rule{Dest: dest, Items: items})
		current = &file.Rules[len(file.Rules)-1]
	}

	return file, nil
}

// parseLine parses a single non-indented rule line.
//
// Lines with an arrow ("->" or "<-") are move rules. Lines without an
// arrow are parsed as rename-only items with an empty destination.
func parseLine(line string) (dest string, items []Item, err error) {
	left, right, arrow, ok := splitArrow(line)
	if !ok {
		// No arrow: rename-only items (e.g. "Foo=Bar" or "T#Field=NewField").
		if !strings.ContainsAny(line, "=#") {
			return "", nil, fmt.Errorf("expected '->' or '<-'")
		}
		items, err = parseItems(line)
		return "", items, err
	}

	switch arrow {
	case "->":
		if right == "" {
			return "", nil, fmt.Errorf("missing destination after '->'")
		}
		if left == "" {
			return "", nil, fmt.Errorf("missing items before '->'")
		}
		items, err = parseItems(left)
		return right, items, err

	case "<-":
		if left == "" {
			return "", nil, fmt.Errorf("missing destination before '<-'")
		}
		if right != "" {
			items, err = parseItems(right)
		}
		return left, items, err
	}

	return "", nil, fmt.Errorf("expected '->' or '<-'")
}

// splitArrow finds the first " -> " or " <- " (or trailing " <-"/"<-") in line.
func splitArrow(line string) (left, right, arrow string, ok bool) {
	for _, op := range []string{" -> ", " <- "} {
		if before, after, ok0 := strings.Cut(line, op); ok0 {
			return strings.TrimSpace(before),
				strings.TrimSpace(after),
				strings.TrimSpace(op),
				true
		}
	}
	// Handle trailing "<-" for multiline rules (e.g. "dest <-" or "dest\t<-").
	if strings.HasSuffix(line, " <-") || strings.HasSuffix(line, "\t<-") {
		return strings.TrimSpace(line[:len(line)-3]), "", "<-", true
	}
	return "", "", "", false
}

// parseItems splits a whitespace-separated list of item tokens.
func parseItems(s string) ([]Item, error) {
	tokens := strings.Fields(s)
	items := make([]Item, 0, len(tokens))
	for _, tok := range tokens {
		item, err := ParseItem(tok)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// ParseItem parses a single item token.
//
// Grammar:
//
//	path/to/file.go                      whole-file move (valid only with a .go dest)
//	[source:]name                        bare reference
//	[source:]name=new                    rename
//	[source:]name=Type#Method            attach function as method
//	[source:]Type#field                  bare field/method reference
//	[source:]Type#field=new              field rename
//	[source:]Type#method=!               detach method (keep name)
//	[source:]Type#method=!new            detach method and rename
//	package.name[...]                    package-qualified form of any above
func ParseItem(tok string) (Item, error) {
	var item Item

	// Whole-file move: a token that looks like a .go file path with no
	// qualifier, rename, or field syntax. Must be paired with a .go dest.
	if strings.HasSuffix(tok, ".go") && !strings.ContainsAny(tok, ":=#") {
		return Item{Source: tok, IsFileMove: true}, nil
	}

	rest := tok
	// Extract source prefix.
	if before, after, ok := strings.Cut(tok, ":"); ok {
		// Explicit file source: "path:Name".
		item.Source = before
		rest = after
	} else if strings.Contains(tok, "/") {
		// Package reference (relative or absolute): split on last "."
		// after last "/" to separate package path from declaration name.
		// Handles both "./pkg.Name" and "github.com/pkg.Name".
		lastSlash := strings.LastIndex(tok, "/")
		dotOff := strings.LastIndex(tok[lastSlash+1:], ".")
		if dotOff >= 0 {
			dot := lastSlash + 1 + dotOff
			item.Source = tok[:dot]
			rest = tok[dot+1:]
		}
	}

	if rest == "" {
		return Item{}, fmt.Errorf("missing name in %q", tok)
	}

	// Route on the relative position of the first '#' and first '='.
	h := strings.Index(rest, "#")
	e := strings.Index(rest, "=")

	switch {
	case h < 0 && e < 0:
		item.Name = rest

	case e < 0:
		// Bare field/method reference: Type#Name.
		item.Name = rest[:h]
		item.Field = rest[h+1:]
		if item.Name == "" {
			return Item{}, fmt.Errorf("missing type before '#' in %q", tok)
		}
		if item.Field == "" {
			return Item{}, fmt.Errorf("missing field name after '#' in %q", tok)
		}

	case h < 0 || e < h:
		// Top-level rename or attach: name=rhs.
		item.Name = rest[:e]
		rhs := rest[e+1:]
		if item.Name == "" {
			return Item{}, fmt.Errorf("missing name before '=' in %q", tok)
		}
		if rhs == "" {
			return Item{}, fmt.Errorf("missing rename after '=' in %q", tok)
		}
		if strings.HasPrefix(rhs, "!") {
			return Item{}, fmt.Errorf("'=!' only valid after a method reference in %q", tok)
		}
		if typ, method, ok := strings.Cut(rhs, "#"); ok {
			// Attach: fn=Type#Method.
			item.MethodOf = typ
			item.Rename = method
			if item.MethodOf == "" {
				return Item{}, fmt.Errorf("missing type before '#' on right side of %q", tok)
			}
			if item.Rename == "" {
				return Item{}, fmt.Errorf("missing method name after '#' on right side of %q", tok)
			}
			if strings.Contains(item.Rename, "#") {
				return Item{}, fmt.Errorf("unexpected '#' in method name %q", item.Rename)
			}
		} else {
			item.Rename = rhs
		}

	default: // h < e: field form with rename or detach
		item.Name = rest[:h]
		item.Field = rest[h+1 : e]
		rhs := rest[e+1:]
		if item.Name == "" {
			return Item{}, fmt.Errorf("missing type before '#' in %q", tok)
		}
		if item.Field == "" {
			return Item{}, fmt.Errorf("missing field name before '=' in %q", tok)
		}
		if strings.HasPrefix(rhs, "!") {
			item.Detach = true
			item.FieldRename = rhs[1:]
			if strings.ContainsAny(item.FieldRename, "#=") {
				return Item{}, fmt.Errorf("unexpected '#' or '=' in detach name %q", item.FieldRename)
			}
		} else {
			item.FieldRename = rhs
			if item.FieldRename == "" {
				return Item{}, fmt.Errorf("missing field rename after '=' in %q", tok)
			}
			if strings.ContainsAny(item.FieldRename, "#=") {
				return Item{}, fmt.Errorf("unexpected '#' or '=' in rename %q", item.FieldRename)
			}
		}
	}

	if item.Name == "" {
		return Item{}, fmt.Errorf("missing name in %q", tok)
	}

	return item, nil
}

// validateFileMove enforces that file-move items appear alone and with a
// .go destination.
func validateFileMove(items []Item, dest string) error {
	hasFileMove := false
	for _, it := range items {
		if it.IsFileMove {
			hasFileMove = true
			break
		}
	}
	if !hasFileMove {
		return nil
	}
	if len(items) != 1 {
		return fmt.Errorf("file-move rule must have exactly one source file")
	}
	if !strings.HasSuffix(dest, ".go") {
		return fmt.Errorf("file-move destination %q must be a .go path", dest)
	}
	return nil
}

// parseDirective checks whether line is a "@key value" or "@key=value" directive.
func parseDirective(line string) (Directive, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "@") {
		return Directive{}, false
	}
	rest := trimmed[1:]
	if rest == "" {
		return Directive{}, false
	}

	// "@key=value" form — only if "=" comes before any whitespace.
	if eqIdx := strings.Index(rest, "="); eqIdx >= 0 {
		spIdx := strings.IndexAny(rest, " \t")
		if spIdx < 0 || eqIdx < spIdx {
			key := rest[:eqIdx]
			if key == "" {
				return Directive{}, false
			}
			return Directive{
				Key:   key,
				Value: rest[eqIdx+1:],
			}, true
		}
	}

	// "@key value" or "@key\tvalue" form.
	key, value, _ := strings.Cut(rest, " ")
	if value == "" {
		key, value, _ = strings.Cut(rest, "\t")
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return Directive{}, false
	}
	return Directive{Key: key, Value: value}, true
}

// stripComment removes the comment portion of a line.
// A '#' starts a comment only when it is at position 0 or preceded by whitespace,
// so that '#' inside tokens (e.g. Name#Field) is preserved.
func stripComment(line string) string {
	for i := range len(line) {
		if line[i] != '#' {
			continue
		}
		if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
			return line[:i]
		}
	}
	return line
}
