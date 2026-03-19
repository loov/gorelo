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
			file.Directives = append(file.Directives, d)
			current = nil
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "@") {
			return nil, fmt.Errorf("%s:%d: invalid directive", filename, lineno)
		}

		line = stripComment(line)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			current = nil
			continue
		}

		indented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
		if indented {
			if current == nil {
				return nil, fmt.Errorf("%s:%d: unexpected indented line", filename, lineno)
			}
			items, err := parseItems(trimmed)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", filename, lineno, err)
			}
			current.Items = append(current.Items, items...)
			continue
		}

		dest, items, err := parseLine(trimmed)
		if err != nil {
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
		if idx := strings.Index(line, op); idx >= 0 {
			return strings.TrimSpace(line[:idx]),
				strings.TrimSpace(line[idx+len(op):]),
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
		item, err := parseItem(tok)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// parseItem parses a single item token.
//
// Grammar:
//
//	[source:]name[=rename]
//	[source:]name#field[=fieldrename]
//	package.name[#field[=fieldrename]]
func parseItem(tok string) (Item, error) {
	var item Item

	rest := tok
	// Extract source prefix.
	if idx := strings.Index(tok, ":"); idx >= 0 {
		// Explicit file source: "path:Name".
		item.Source = tok[:idx]
		rest = tok[idx+1:]
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

	// Split field path from name.
	if hashIdx := strings.Index(rest, "#"); hashIdx >= 0 {
		item.Name = rest[:hashIdx]
		fieldSpec := rest[hashIdx+1:]
		if fieldSpec == "" {
			return Item{}, fmt.Errorf("missing field name after '#' in %q", tok)
		}
		if eqIdx := strings.Index(fieldSpec, "="); eqIdx >= 0 {
			item.Field = fieldSpec[:eqIdx]
			item.FieldRename = fieldSpec[eqIdx+1:]
			if item.Field == "" {
				return Item{}, fmt.Errorf("missing field name before '=' in %q", tok)
			}
			if item.FieldRename == "" {
				return Item{}, fmt.Errorf("missing field rename after '=' in %q", tok)
			}
		} else {
			item.Field = fieldSpec
		}
	} else if eqIdx := strings.Index(rest, "="); eqIdx >= 0 {
		item.Name = rest[:eqIdx]
		item.Rename = rest[eqIdx+1:]
		if item.Rename == "" {
			return Item{}, fmt.Errorf("missing rename after '=' in %q", tok)
		}
	} else {
		item.Name = rest
	}

	if item.Name == "" {
		return Item{}, fmt.Errorf("missing name in %q", tok)
	}

	return item, nil
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
