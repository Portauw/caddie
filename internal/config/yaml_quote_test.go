package config

import "testing"

func TestYAMLQuoteRoundTrip(t *testing.T) {
	cases := []string{
		"plain",
		`has "quotes" inside`,
		`trailing backslash\`,
		"line1\nline2",
		"a\ttab",
		`mixed \"escape\" already`,
		`C:\Users\foo`, // backslash unrelated to our escaping — must survive untouched
		"",
	}
	for _, want := range cases {
		quoted := YAMLQuote(want)
		got := StripQuotes(quoted)
		if got != want {
			t.Errorf("round trip failed: input %q -> quoted %q -> got %q", want, quoted, got)
		}
	}
}

func TestYAMLQuotePreventsInjection(t *testing.T) {
	// A name containing a literal `"` and newline must not be able to
	// terminate the quoted scalar and start a new YAML line.
	malicious := "legit\"\n    prefix: \"true"
	quoted := YAMLQuote(malicious)
	if got := StripQuotes(quoted); got != malicious {
		t.Errorf("got %q want %q", got, malicious)
	}
	// The quoted form itself must not contain a raw, unescaped double quote
	// or newline anywhere except the opening/closing delimiters.
	inner := quoted[1 : len(quoted)-1]
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '"':
			t.Fatalf("unescaped quote leaked into scalar body: %q", quoted)
		case '\n':
			t.Fatalf("raw newline leaked into scalar body: %q", quoted)
		case '\\':
			i++ // skip the escaped character
		}
	}
}

// TestYAMLQuoteControlCharacters pins that no control character survives into
// a quoted scalar. cmdInit's readLine trims only "\n", so CRLF-piped stdin
// leaves a bare CR on the value; written raw it round-tripped back out and
// mangled every line that printed the name.
func TestYAMLQuoteControlCharacters(t *testing.T) {
	for _, v := range []string{
		"trailing cr\r",
		"crlf\r\nsecond",
		"bell\a and vtab\v",
		"del\x7f",
		"nul\x00byte",
		"tab\tand newline\n",
		`quote " and backslash \`,
		"plain value",
		"unicode é ☕",
	} {
		quoted := YAMLQuote(v)
		for _, r := range quoted {
			if r < 0x20 || r == 0x7f {
				t.Errorf("YAMLQuote(%q) = %q, still contains a raw control character", v, quoted)
				break
			}
		}
		if got := StripQuotes(quoted); got != v {
			t.Errorf("round trip of %q gave %q", v, got)
		}
	}
}
