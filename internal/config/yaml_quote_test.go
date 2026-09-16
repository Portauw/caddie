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
