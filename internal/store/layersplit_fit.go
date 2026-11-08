package store

import "fmt"

// Проверка, влезает ли модель в группу узлов.
//
// Зачем отдельная функция. Раскладка по видеопамяти (SplitLayersByCapacity)
// делит слои пропорционально картам, но ничего не говорит о том, хватает ли
// суммы. Если модель больше суммы карт, каждый узел молча получает больше,
// чем может держать, и падение случается уже внутри llama.cpp
// (`cudaMalloc failed`) — то есть там, где причина не видна ни участнику, ни
// панели.
//
// Здесь считается то же самое заранее и с числами: сколько нужно, сколько есть
// и чего не хватает. Вызывающий код может отказать понятным текстом вместо
// запуска группы, которая заведомо не поднимется.

// GroupFit — ответ на вопрос «хватит ли этой группе памяти на модель».
type GroupFit struct {
	// Known — все ли узлы сообщили объём карты. Если хотя бы один не сообщил,
	// судить нельзя: отказ по неполным данным запретил бы заведомо рабочую
	// группу.
	Known bool `json:"known"`
	// Fits — влезает ли модель целиком. Осмысленно только при Known.
	Fits bool `json:"fits"`
	// Layers — сколько слоёв у модели.
	Layers int `json:"layers"`
	// NeededBytes — веса плюс KV-кэш на заданный контекст.
	NeededBytes uint64 `json:"needed_bytes"`
	// CapacityBytes — полезная видеопамять группы: сумма карт минус запас.
	CapacityBytes uint64 `json:"capacity_bytes"`
	// MissingBytes — сколько байт не хватает. Ноль, если влезает.
	MissingBytes uint64 `json:"missing_bytes"`
	// MissingLayers — сколько слоёв некуда положить. Округление вверх: один
	// слой это целая единица памяти.
	MissingLayers int `json:"missing_layers"`
	// MaxLayers — предел слоёв по памяти для каждого узла, в порядке входа.
	// Нужен раскладке, чтобы ни один узел не получил больше своего предела.
	MaxLayers []int `json:"max_layers"`
}

// PlanGroupFit считает, влезает ли модель в группу.
//
// kvBytesPerToken — KV-кэш всей модели на один токен (байты); для контекста
// он умножается на ctxTokens. Если KV неизвестен, передаётся ноль: тогда
// проверка учитывает только веса и честно называется нижней оценкой.
func PlanGroupFit(layers int, modelBytes, kvBytesPerToken uint64, ctxTokens int, cardBytes []uint64) GroupFit {
	// Без слоёв, веса или карт считать нечего: пустой ответ честнее
	// выдуманного «не влезает».
	if layers <= 0 || modelBytes == 0 || len(cardBytes) == 0 {
		return GroupFit{}
	}
	fit := GroupFit{Layers: layers}
	if ctxTokens < 0 {
		ctxTokens = 0
	}

	// Кэш на один слой: общий KV на токен делится поровну между слоями.
	kvPerLayer := uint64(0)
	if kvBytesPerToken > 0 && layers > 0 {
		kvPerLayer = kvBytesPerToken / uint64(layers)
	}
	if kvPerLayer == 0 && kvBytesPerToken > 0 {
		kvPerLayer = 1
	}
	perLayer := modelBytes/uint64(layers) + kvPerLayer*uint64(ctxTokens)
	if perLayer == 0 {
		perLayer = 1
	}

	fit.MaxLayers = make([]int, len(cardBytes))
	fit.NeededBytes = perLayer * uint64(layers)

	known := true
	for index, card := range cardBytes {
		if card == 0 {
			known = false
			continue
		}
		usable := vramForSplit(card)
		fit.CapacityBytes += usable
		maxLayers := int(usable / perLayer)
		if maxLayers > layers {
			maxLayers = layers
		}
		fit.MaxLayers[index] = maxLayers
	}
	fit.Known = known

	covered := 0
	for _, maxLayers := range fit.MaxLayers {
		covered += maxLayers
	}
	if covered >= layers {
		fit.Fits = true
		if !known {
			// Часть карт неизвестна: сумма заведомо не меньше посчитанной,
			// поэтому «влезает» — честный ответ, а не догадка.
			fit.Fits = fit.CapacityBytes >= fit.NeededBytes
		}
		return fit
	}

	fit.MissingLayers = layers - covered
	fit.MissingBytes = uint64(fit.MissingLayers) * perLayer
	return fit
}

// MissingText — короткий человеческий текст для журнала и панели.
func (f GroupFit) MissingText() string {
	if !f.Known || !f.LayersSet() {
		return "недостаточно данных о картах, чтобы судить"
	}
	if f.Fits {
		return "хватает"
	}
	return fmt.Sprintf("не хватает %d слоёв (%.1f ГиБ): нужно %.1f ГиБ, есть %.1f ГиБ",
		f.MissingLayers,
		float64(f.MissingBytes)/(1<<30),
		float64(f.NeededBytes)/(1<<30),
		float64(f.CapacityBytes)/(1<<30))
}

// LayersSet — была ли вообще задача считать (модель известна, карты переданы).
func (f GroupFit) LayersSet() bool {
	return f.Layers > 0
}

// ModelRefusal — текст отказа для панели и журнала: модель не влезает в группу.
func ModelRefusal(model string, fit GroupFit) string {
	return fmt.Sprintf("модель %s не помещается в группу компьютеров: %s", model, fit.MissingText())
}
