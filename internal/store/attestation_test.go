package store

import "testing"

// Digest приходит из рантайма с префиксом, а в манифесте лежит без него.
func TestNormalizeDigest(t *testing.T) {
	cases := map[string]string{
		"sha256:ABCDEF":     "abcdef",
		"  SHA256:abcdef  ": "abcdef",
		"abcdef":            "abcdef",
		"sha256:":           "",
		"":                  "",
	}
	for input, want := range cases {
		if got := NormalizeDigest(input); got != want {
			t.Fatalf("NormalizeDigest(%q) = %q, ожидалось %q", input, got, want)
		}
	}
}

// Сверка весов: совпадение только при непустых одинаковых хэшах.
func TestDigestsMatch(t *testing.T) {
	cases := []struct {
		name     string
		expected string
		observed string
		want     bool
	}{
		{"одинаковые", "500a1f067a9f", "500a1f067a9f", true},
		{"с префиксом у узла", "500a1f067a9f", "sha256:500a1f067a9f", true},
		{"регистр не важен", "500A1F067A9F", "500a1f067a9f", true},
		{"разные веса", "500a1f067a9f", "111111111111", false},
		{"узел промолчал", "500a1f067a9f", "", false},
		{"нет манифеста", "", "500a1f067a9f", false},
		{"оба пустые", "", "", false},
	}
	for _, testCase := range cases {
		if got := DigestsMatch(testCase.expected, testCase.observed); got != testCase.want {
			t.Fatalf("%s: DigestsMatch(%q, %q) = %v, ожидалось %v",
				testCase.name, testCase.expected, testCase.observed, got, testCase.want)
		}
	}
}
