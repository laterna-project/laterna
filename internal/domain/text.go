package domain

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Text is a message for a person that the server does not write in any language: a message key and
// its params. Whoever displays it translates it, either the client with its own catalogs or the
// server (internal/i18n) when it has to render the text itself.
type Text struct {
	// Key is the stable message key ("reason.video_unsupported"): lower case, families separated by
	// dots.
	Key string `json:"key"`
	// Params are named. Numbers are decimal, durations in seconds ("..._seconds"), sizes in bytes
	// ("..._bytes").
	Params map[string]string `json:"params,omitempty"`
	// List holds texts that are rendered one by one and joined in place of {list} (reasons for a
	// refusal, changes to a setting).
	List []Text `json:"list,omitempty"`
}

// LiteralKey is the key of a text that is already written and will not be translated. Its only
// param, "text", is that text.
const LiteralKey = "literal"

// Literal wraps an already written sentence: something another server answered, or something
// Laterna stored before its texts were keyed.
func Literal(s string) Text {
	return Text{Key: LiteralKey, Params: map[string]string{"text": strings.ToValidUTF8(s, "\uFFFD")}}
}

// UnmarshalJSON reads a text from the database. A bare string is a sentence stored before texts
// were keyed and becomes a literal.
func (t *Text) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*t = Literal(s)
		return nil
	}
	type plain Text
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*t = Text(p)
	return nil
}

// T builds a text from its key followed by name/value pairs. A []Text value, given without a name,
// is its list.
func T(key string, kv ...any) Text {
	t := Text{Key: key}
	for i := 0; i < len(kv); i++ {
		if list, ok := kv[i].([]Text); ok {
			t.List = append(t.List, list...)
			continue
		}
		name, ok := kv[i].(string)
		if !ok || i+1 == len(kv) {
			name, i = "!BADKEY", i-1 // like slog: keep the value visible
		}
		if t.Params == nil {
			t.Params = make(map[string]string, len(kv)/2)
		}
		t.Params[name] = paramValue(kv[i+1])
		i++
	}
	return t
}

// IsZero reports an empty text.
func (t Text) IsZero() bool { return t.Key == "" }

// String prints the text untranslated: its key and params, sorted by name. For logs and tests.
func (t Text) String() string {
	if len(t.Params) == 0 && len(t.List) == 0 {
		return t.Key
	}
	var b strings.Builder
	b.WriteString(t.Key)
	b.WriteString(" (")
	for i, name := range slices.Sorted(maps.Keys(t.Params)) {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name + "=" + t.Params[name])
	}
	for i, sub := range t.List {
		if i > 0 || len(t.Params) > 0 {
			b.WriteString(", ")
		}
		b.WriteString(sub.String())
	}
	b.WriteString(")")
	return b.String()
}

// paramValue formats a param value: a duration in seconds (rounded up), anything else the way fmt
// prints it. The result is always valid UTF-8, because a path read from disk may not be and
// Protobuf would reject it.
func paramValue(v any) string {
	var s string
	switch v := v.(type) {
	case string:
		s = v
	case time.Duration:
		s = strconv.FormatInt(int64(math.Ceil(v.Seconds())), 10)
	case error:
		s = v.Error()
	default:
		s = fmt.Sprint(v)
	}
	return strings.ToValidUTF8(s, "�")
}
