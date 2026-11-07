package httpapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Чанки должны покрывать текст целиком и не терять факты на границах.
func TestSplitIntoChunksCoversText(t *testing.T) {
	paragraph := "Первое правило: сервис не хранит ключи в открытом виде. "
	text := strings.Repeat(paragraph+"\n\n", 30)

	chunks := splitIntoChunks(text)
	if len(chunks) < 2 {
		t.Fatalf("ожидалось несколько чанков, получено %d", len(chunks))
	}
	for index, chunk := range chunks {
		if len(chunk) > chunkChars+chunkOverlap {
			t.Fatalf("чанк %d слишком длинный: %d символов", index, len(chunk))
		}
		if strings.TrimSpace(chunk) == "" {
			t.Fatalf("чанк %d пустой", index)
		}
	}

	// Каждая часть исходного абзаца должна присутствовать хотя бы в одном чанке.
	joined := strings.Join(chunks, " ")
	if !strings.Contains(joined, "ключи в открытом виде") {
		t.Fatal("содержимое потеряно при разбиении")
	}
}

// Очень длинный абзац без пустых строк режется по предложениям.
func TestSplitLongParagraph(t *testing.T) {
	sentence := "Это предложение нужно для проверки нарезки длинного текста. "
	paragraph := strings.Repeat(sentence, 60) // заметно больше chunkChars

	pieces := splitLongParagraph(paragraph)
	if len(pieces) < 2 {
		t.Fatalf("длинный абзац не разрезан: %d частей", len(pieces))
	}
	for index, piece := range pieces {
		if len(piece) > chunkChars {
			t.Fatalf("часть %d длиннее чанка: %d", index, len(piece))
		}
	}
}

// Короткий текст остаётся одним чанком: дробить нечего.
func TestSplitIntoChunksShortText(t *testing.T) {
	chunks := splitIntoChunks("Короткая заметка про лицензии и ключи.")
	if len(chunks) != 1 {
		t.Fatalf("ожидался один чанк, получено %d", len(chunks))
	}
}

// Заголовок по умолчанию: первая строка, обрезанная по границе руны.
func TestFirstLine(t *testing.T) {
	if got := firstLine("Заголовок\nостальное", 80); got != "Заголовок" {
		t.Fatalf("заголовок: %q", got)
	}
	long := strings.Repeat("у", 100)
	got := firstLine(long, 10)
	if len([]rune(strings.TrimSuffix(got, "…"))) != 10 {
		t.Fatalf("обрезанный заголовок: %q (%d рун)", got, len([]rune(got)))
	}
	if got := firstLine("   ", 10); got != "Документ" {
		t.Fatalf("пустой текст должен давать «Документ», получено %q", got)
	}
}

// Кириллица на стыке чанков не должна превращаться в битые байты: Postgres
// отвергает невалидный UTF-8, и документ было бы невозможно сохранить.
func TestSplitIntoChunksProducesValidUTF8(t *testing.T) {
	paragraph := "Правило резервирования требует дублирования канала связи и проверки состояния узла. "
	text := strings.Repeat(paragraph+"\n\n", 25)

	for index, chunk := range splitIntoChunks(text) {
		if !utf8.ValidString(chunk) {
			t.Fatalf("чанк %d содержит невалидный UTF-8: %q", index, chunk)
		}
	}
	if got := tailRunes("абвгде", 3); got != "где" {
		t.Fatalf("tailRunes вернул %q", got)
	}
	if got := tailRunes("аб", 10); got != "аб" {
		t.Fatalf("tailRunes короткой строки вернул %q", got)
	}
}
