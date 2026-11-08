package store

import (
	"strings"
	"testing"
)

const gib = uint64(1) << 30

// Случай владельца: модель 41 ГиБ, 80 слоёв, карты 8 + 16 + 16 ГиБ.
// Суммы не хватает, и проверка обязана это сказать числами, а не молчать.
func TestPlanGroupFitRefusesModelBiggerThanGroup(t *testing.T) {
	modelBytes := uint64(41) * gib
	fit := PlanGroupFit(80, modelBytes, 0, 0, []uint64{8 * gib, 16 * gib, 16 * gib})

	if !fit.Known {
		t.Fatal("карты известны, Known должен быть true")
	}
	if fit.Fits {
		t.Fatalf("41 ГиБ не влезают в 8+16+16 ГиБ: %s", fit.MissingText())
	}
	if fit.MissingLayers <= 0 {
		t.Fatalf("MissingLayers = %d, ожидалось больше нуля", fit.MissingLayers)
	}
	// Полезная ёмкость: (8-1.5) + (16-1.5) + (16-1.5) = 35.5 ГиБ.
	if fit.CapacityBytes != 355*1024*1024*1024/10 {
		t.Fatalf("CapacityBytes = %d, ожидалось 35.5 ГиБ", fit.CapacityBytes)
	}
	if fit.MissingBytes == 0 {
		t.Fatal("MissingBytes должен быть положительным")
	}
	if text := fit.MissingText(); text == "" || !strings.Contains(text, "не хватает") {
		t.Fatalf("текст отказа = %q", text)
	}
}

// Четыре карты по 16 ГиБ держат ту же модель с запасом.
func TestPlanGroupFitAcceptsEnoughCapacity(t *testing.T) {
	fit := PlanGroupFit(80, uint64(41)*gib, 0, 0, []uint64{16 * gib, 16 * gib, 16 * gib, 16 * gib})

	if !fit.Fits {
		t.Fatalf("58 ГиБ полезной ёмкости должны держать 41 ГиБ: %s", fit.MissingText())
	}
	if fit.MissingBytes != 0 || fit.MissingLayers != 0 {
		t.Fatalf("при достаточной ёмкости недостачи быть не может: %+v", fit)
	}
}

// KV-кэш учитывается: та же модель и ёмкость, но контекст 16K может
// перевернуть ответ с «влезает» на «не влезает».
func TestPlanGroupFitCountsKVCache(t *testing.T) {
	modelBytes := uint64(24) * gib
	cards := []uint64{16 * gib, 16 * gib}

	// KV на токен для 80 слоёв: 80 * 2 * 8 голов * 128 * 1 байт ≈ 160 КиБ.
	const kvPerToken = 80 * 2 * 8 * 128

	small := PlanGroupFit(80, modelBytes, kvPerToken, 2048, cards)
	if !small.Fits {
		t.Fatalf("с окном 2048 модель должна влезать: %s", small.MissingText())
	}

	large := PlanGroupFit(80, modelBytes, kvPerToken, 65536, cards)
	if large.Fits {
		t.Fatalf("с окном 65536 KV не должен помещаться: %s", large.MissingText())
	}
	if large.NeededBytes <= small.NeededBytes {
		t.Fatal("нужно больше памяти при большем окне")
	}
}

// Неизвестный объём карты — не повод отказывать: судить нельзя, и это
// сказано явно.
func TestPlanGroupFitUnknownCapacity(t *testing.T) {
	fit := PlanGroupFit(80, uint64(41)*gib, 0, 0, []uint64{8 * gib, 0, 16 * gib})

	if fit.Known {
		t.Fatal("одна карта неизвестна, Known должен быть false")
	}
	if fit.MaxLayers[1] != 0 {
		t.Fatalf("у неизвестной карты предел должен быть 0, а не %d", fit.MaxLayers[1])
	}
	if text := fit.MissingText(); !strings.Contains(text, "недостаточно данных") {
		t.Fatalf("текст = %q", text)
	}
}

// Предел слоёв по памяти не превышает то, что влезает на карту: 5 ГиБ веса
// на карту 8 ГиБ — это не больше 6 слоёв по ~0.85 ГиБ.
func TestPlanGroupFitMaxLayersRespectsReserve(t *testing.T) {
	// 80 слоёв, 68 ГиБ весов → 0.85 ГиБ на слой; полезная ёмкость карты 8 ГиБ
	// равна 6.5 ГиБ, то есть 7 слоёв максимум.
	fit := PlanGroupFit(80, uint64(68)*gib, 0, 0, []uint64{8 * gib})
	if fit.MaxLayers[0] > 7 {
		t.Fatalf("MaxLayers = %d, карта 8 ГиБ не может держать столько", fit.MaxLayers[0])
	}
	if fit.MaxLayers[0] < 5 {
		t.Fatalf("MaxLayers = %d, слишком мало для 6.5 ГиБ полезной памяти", fit.MaxLayers[0])
	}
}

// Пустые входы не должны паниковать: узел без карты, модель без слоёв.
func TestPlanGroupFitEmptyInputs(t *testing.T) {
	if fit := PlanGroupFit(0, gib, 0, 0, []uint64{16 * gib}); fit.LayersSet() {
		t.Fatal("без слоёв считать нечего")
	}
	if fit := PlanGroupFit(80, gib, 0, 0, nil); fit.LayersSet() {
		t.Fatal("без карт считать нечего")
	}
}

// Текст отказа должен быть понятен человеку: модель, недостача в слоях и
// байтах, ёмкость группы. Его показывает панель вместо ошибки видеокарты.
func TestModelRefusalTextNamesTheDeficit(t *testing.T) {
	fit := PlanGroupFit(80, uint64(41)*gib, 0, 0, []uint64{8 * gib, 16 * gib, 16 * gib})
	text := ModelRefusal("qwen2.5:72b-instruct-q4_0", fit)

	for _, want := range []string{"qwen2.5:72b-instruct-q4_0", "не помещается", "не хватает"} {
		if !strings.Contains(text, want) {
			t.Fatalf("в тексте нет %q: %s", want, text)
		}
	}
}

// Когда модель помещается, отказа быть не должно — иначе рабочую группу
// запретило бы собственное сообщение.
func TestModelRefusalIsPositiveWhenItFits(t *testing.T) {
	fit := PlanGroupFit(80, uint64(20)*gib, 0, 0, []uint64{16 * gib, 16 * gib})
	if !fit.Fits {
		t.Fatalf("20 ГиБ должны влезать в две карты по 16 ГиБ: %s", fit.MissingText())
	}
	if text := ModelRefusal("gpt-oss:20b", fit); !strings.Contains(text, "хватает") {
		t.Fatalf("текст для влазящей модели = %q", text)
	}
}
