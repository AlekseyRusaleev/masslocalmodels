package worker

import "testing"

func TestModelFitsCardNeedsTheWholeFileAndCache(t *testing.T) {
	const weights = 14 << 30
	if ModelFitsCard(8<<30, weights) {
		t.Fatal("8 ГиБ не вмещают файл 14 ГиБ")
	}
	if !ModelFitsCard(24<<30, weights) {
		t.Fatal("24 ГиБ вмещают файл 14 ГиБ и запас под кэш")
	}
	if ModelFitsCard(15<<30, weights) {
		t.Fatal("15 ГиБ оставляют только 1 ГиБ, этого мало для кэша")
	}
	if ModelFitsCard(0, weights) || ModelFitsCard(24<<30, 0) {
		t.Fatal("пустая карта или пустой файл не являются исключением")
	}
}
