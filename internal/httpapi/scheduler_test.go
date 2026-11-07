package httpapi

import (
	"net/http/httptest"
	"testing"
)

// Подмена модели должна обнаруживаться: клиент просил одно, узел обслужил другое.
func TestModelMismatch(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		served    string
		want      bool
	}{
		{"совпадает", "qwen3:8b", "qwen3:8b", false},
		{"регистр не важен", "Qwen3:8B", "qwen3:8b", false},
		{"latest прощается", "qwen3:8b", "qwen3:8b:latest", false},
		{"пробелы прощаются", " qwen3:8b ", "qwen3:8b", false},
		{"другая модель", "qwen3:8b", "qwen3:14b", true},
		{"другое семейство", "qwen3:8b", "llama3:8b", true},
		{"узел промолчал", "qwen3:8b", "", false},
	}
	for _, testCase := range cases {
		got := modelMismatch(testCase.requested, testCase.served) != ""
		if got != testCase.want {
			t.Fatalf("%s: modelMismatch(%q, %q) = %v, ожидалось %v",
				testCase.name, testCase.requested, testCase.served, got, testCase.want)
		}
	}
}

// Приоритет приходит заголовком: мусор не должен ломать очередь.
func TestRequestPriority(t *testing.T) {
	cases := map[string]int{
		"":     0,
		"7":    7,
		"-5":   -5,
		"101":  0,
		"abc":  0,
		" 12 ": 12,
	}
	for header, want := range cases {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if header != "" {
			r.Header.Set("X-Compute-Priority", header)
		}
		if got := requestPriority(r); got != want {
			t.Fatalf("priority %q = %d, ожидалось %d", header, got, want)
		}
	}
}
