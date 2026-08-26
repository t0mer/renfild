package intent

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"lowercases", "Turn ON the Lights", "turn on the lights"},
		{"strips punctuation", "what's the time?", "whats the time"},
		{"collapses whitespace", "  turn   on \n lights  ", "turn on lights"},
		{"keeps digits", "set it to 21 degrees", "set it to 21 degrees"},
		{"keeps Hebrew untouched", "מה השעה?", "מה השעה"},
		{"keeps Hebrew with punctuation between words", "שלום, מה נשמע!", "שלום מה נשמע"},
		{"drops emoji and symbols", "hello 👋 world €", "hello world"},
		{"empty stays empty", "   ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.input); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTruncateWords(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  string
	}{
		{"under the limit", "short answer", 5, "short answer"},
		{"exactly the limit", "one two three", 3, "one two three"},
		{"over the limit", "one two three four", 3, "one two three."},
		{"already punctuated", "one two three. four", 3, "one two three."},
		{"no limit", "one two three four", 0, "one two three four"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := TruncateWords(tc.text, tc.limit); got != tc.want {
				t.Fatalf("TruncateWords(%q, %d) = %q, want %q", tc.text, tc.limit, got, tc.want)
			}
		})
	}
}
