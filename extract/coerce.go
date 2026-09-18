package extract

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Coerce applies a named x-magpie coercion to a string value.
func Coerce(kind, in string) (any, error) {
	switch kind {
	case "trim":
		return strings.TrimSpace(in), nil
	case "int":
		s := strings.TrimSpace(in)
		if i, err := strconv.Atoi(s); err == nil {
			return i, nil
		}
		if f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64); err == nil {
			return int(f), nil
		}
		return nil, fmt.Errorf("extract: coerce int from %q", in)
	case "float":
		s := strings.TrimSpace(strings.ReplaceAll(in, ",", "."))
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("extract: coerce float from %q", in)
		}
		return f, nil
	case "eur_decimal":
		// Strip €, nbsp, thin/narrow spaces; comma → dot.
		s := strings.TrimSpace(in)
		s = strings.ReplaceAll(s, "€", "")
		for _, sp := range []string{"\u00a0", "\u202f", "\u2009", " ", "'"} {
			s = strings.ReplaceAll(s, sp, "")
		}
		s = strings.ReplaceAll(s, ",", ".")
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("extract: coerce eur_decimal from %q", in)
		}
		return f, nil
	case "bool":
		s := strings.ToLower(strings.TrimSpace(in))
		switch s {
		case "true", "yes", "ja", "oui", "1", "y", "j":
			return true, nil
		case "false", "no", "nein", "non", "0", "n":
			return false, nil
		}
		return nil, fmt.Errorf("extract: coerce bool from %q", in)
	case "iso_date":
		s := strings.TrimSpace(in)
		for _, layout := range []string{"2006-01-02", time.RFC3339, "02.01.2006", "02/01/2006", "01/02/2006", "2.1.2006", "January 2, 2006", "2 Jan 2006"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.Format("2006-01-02"), nil
			}
		}
		if m := regexp.MustCompile(`(\d{1,2})\.(\d{1,2})\.(\d{4})`).FindStringSubmatch(s); m != nil {
			d, derr := strconv.Atoi(m[1])
			mo, merr := strconv.Atoi(m[2])
			if derr != nil || merr != nil {
				return nil, fmt.Errorf("extract: coerce iso_date from %q", in)
			}
			return fmt.Sprintf("%s-%02d-%02d", m[3], mo, d), nil
		}
		return nil, fmt.Errorf("extract: coerce iso_date from %q", in)
	default:
		return nil, fmt.Errorf("extract: unknown coerce %q", kind)
	}
}

// ApplyRegex returns the first capture group (or full match) of pattern in s.
func ApplyRegex(pattern, s string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("extract: bad regex %q: %w", pattern, err)
	}
	m := re.FindStringSubmatch(s)
	if m == nil {
		return "", fmt.Errorf("extract: regex %q matched nothing", pattern)
	}
	if len(m) > 1 {
		return m[1], nil
	}
	return m[0], nil
}

// ApplyCoercions walks record fields and applies regex + coerce hints.
func ApplyCoercions(rec map[string]any, h FieldHints) (map[string]any, error) {
	out := make(map[string]any, len(rec))
	for k, v := range rec {
		s, isStr := v.(string)
		if pat, ok := h.Regex[k]; ok && isStr {
			nv, err := ApplyRegex(pat, s)
			if err != nil {
				return nil, err
			}
			s, v = nv, nv
		}
		if kind, ok := h.Coerce[k]; ok && isStr {
			if kind == "regex" {
				continue
			}
			nv, err := Coerce(kind, s)
			if err != nil {
				return nil, err
			}
			v = nv
		}
		if h.Trim[k] {
			if s, ok := v.(string); ok {
				v = strings.TrimSpace(s)
			}
		}
		out[k] = v
	}
	return out, nil
}
