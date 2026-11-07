// Package scheduler — прогноз скорости топологии.
//
// Модель намеренно простая и объяснимая: при конвейерной генерации каждый токен
// последовательно проходит все стадии, поэтому время на токен складывается из
// суммарных вычислений и сетевых переходов между стадиями.
//
//	token_ms = compute_ms(params, quantization) + hops * rtt_ms
//	tokens_per_second = 1000 / token_ms
//
// Константы калиброваны по измерению на RTX 5060: Qwen3-8B Q4_K_M даёт 70 tok/s,
// то есть 14,3 мс на токен, или ~1.79 мс на 1B параметров при Q4.
package scheduler

import (
	"fmt"
	"math"
)

const (
	// msPerBillionParams — время декода на 1 млрд параметров в Q4 на одной RTX 5060.
	msPerBillionParams = 1.79
	// quantizationFactor — поправка на формат весов относительно Q4.
	quantizationFactorQ8 = 1.6
	quantizationFactorQ5 = 1.15
	quantizationFactorQ3 = 0.8
)

// Params — вход прогноза.
type Params struct {
	// ModelBillions — размер модели в миллиардах параметров.
	ModelBillions float64
	// Quantization — «Q4», «Q5», «Q8», «Q3»; всё остальное считается как Q4.
	Quantization string
	// Stages — параметры стадий конвейера. Если пусто, считается одна стадия.
	Stages []StagePerf
	// RTTMillis — типичный RTT между соседними стадиями.
	RTTMillis float64
	// SplitMode — «layer» (конвейер) или «tensor» (быстрый локальный линк).
	SplitMode string
}

// StagePerf — производительность одной стадии.
//
// LayerShare — доля слоёв модели на этой стадии (сумма по стадиям = 1).
// MSPerBillion — измеренная скорость узла: мс декода на 1 млрд параметров.
// Ноль означает «не откалиброван»: берётся константа RTX 5060.
type StagePerf struct {
	LayerShare   float64
	MSPerBillion float64
}

// Homogeneous — набор одинаковых стадий, удобно для тестов и оценок «на глаз».
func Homogeneous(stages int, msPerBillion float64) []StagePerf {
	if stages < 1 {
		stages = 1
	}
	share := 1 / float64(stages)
	out := make([]StagePerf, 0, stages)
	for i := 0; i < stages; i++ {
		out = append(out, StagePerf{LayerShare: share, MSPerBillion: msPerBillion})
	}
	return out
}

// Estimate — результат.
type Estimate struct {
	TokensPerSecond  float64 `json:"expected_tokens_per_second"`
	TokenMS          float64 `json:"token_ms"`
	ComputeMS        float64 `json:"compute_ms"`
	NetworkMS        float64 `json:"network_ms"`
	Hops             int     `json:"hops"`
	Class            string  `json:"class"`
	Note             string  `json:"note,omitempty"`
	AssumedRTTMillis float64 `json:"assumed_rtt_ms"`
}

// EstimateSpeed считает ожидаемую скорость одиночного запроса.
func EstimateSpeed(p Params) (Estimate, error) {
	if p.ModelBillions <= 0 {
		return Estimate{}, fmt.Errorf("scheduler: model size must be positive")
	}

	stages := p.Stages
	if len(stages) == 0 {
		stages = Homogeneous(1, 0)
	}

	rtt := p.RTTMillis
	if rtt < 0 {
		rtt = 0
	}

	quant := quantFactor(p.Quantization)

	// Вычисления по стадиям: доля слоёв × скорость конкретного узла.
	// Неизвестная скорость узла заменяется калибровкой RTX 5060.
	compute := 0.0
	for _, stage := range stages {
		share := stage.LayerShare
		if share <= 0 {
			share = 1 / float64(len(stages))
		}
		msPerB := stage.MSPerBillion
		if msPerB <= 0 {
			msPerB = msPerBillionParams
		}
		compute += p.ModelBillions * share * msPerB * quant
	}

	hops := len(stages) - 1
	network := float64(hops) * rtt

	// Tensor parallel делит вычисления между картами, поэтому на быстром линке
	// выигрыш реальный; накладные расходы обмена считаем двойным RTT на слой.
	class := "wan"
	if p.SplitMode == "tensor" {
		class = "lan"
		compute = compute / float64(len(stages))
		network = float64(hops) * rtt * 2
	}

	tokenMS := compute + network
	if tokenMS <= 0 {
		return Estimate{}, fmt.Errorf("scheduler: impossible estimate")
	}

	estimate := Estimate{
		TokensPerSecond:  math.Round(1000/tokenMS*100) / 100,
		TokenMS:          math.Round(tokenMS*10) / 10,
		ComputeMS:        math.Round(compute*10) / 10,
		NetworkMS:        math.Round(network*10) / 10,
		Hops:             hops,
		Class:            class,
		AssumedRTTMillis: rtt,
	}
	if rtt == 0 && hops > 0 {
		estimate.Note = "RTT не измерен: прогноз оптимистичен, нужен замер линка"
	}
	if class == "lan" {
		estimate.Note = "tensor parallel: выигрыш зависит от реальной полосы линка"
	}
	return estimate, nil
}

// quantFactor возвращает поправку на формат весов.
func quantFactor(quantization string) float64 {
	switch quantization {
	case "Q8", "Q8_0", "q8", "q8_0":
		return quantizationFactorQ8
	case "Q5", "Q5_K_M", "q5", "q5_k_m":
		return quantizationFactorQ5
	case "Q3", "Q3_K_M", "q3", "q3_k_m":
		return quantizationFactorQ3
	default:
		return 1
	}
}
