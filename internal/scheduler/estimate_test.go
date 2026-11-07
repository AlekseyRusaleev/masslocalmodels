package scheduler

import "testing"

func TestEstimateSingleNodeMatchesMeasurement(t *testing.T) {
	// Измерено: Qwen3-8B Q4 на RTX 5060 даёт 70 tok/s.
	got, err := EstimateSpeed(Params{ModelBillions: 8, Quantization: "Q4", Stages: Homogeneous(1, 0), RTTMillis: 0})
	if err != nil {
		t.Fatalf("EstimateSpeed: %v", err)
	}
	if got.Hops != 0 || got.NetworkMS != 0 {
		t.Fatalf("single stage must have no network cost: %+v", got)
	}
	if got.TokensPerSecond < 65 || got.TokensPerSecond > 75 {
		t.Fatalf("tokens/s = %.2f, want ~70 (measured)", got.TokensPerSecond)
	}
}

func TestEstimatePipelineAddsRTTPerHop(t *testing.T) {
	base, _ := EstimateSpeed(Params{ModelBillions: 32, Quantization: "Q4", Stages: Homogeneous(1, 0)})
	two, _ := EstimateSpeed(Params{ModelBillions: 32, Quantization: "Q4", Stages: Homogeneous(2, 0), RTTMillis: 10})

	if two.Hops != 1 {
		t.Fatalf("hops = %d, want 1", two.Hops)
	}
	if two.NetworkMS != 10 {
		t.Fatalf("network ms = %.1f, want 10", two.NetworkMS)
	}
	if two.TokensPerSecond >= base.TokensPerSecond {
		t.Fatalf("pipeline over WAN must be slower: %.2f vs %.2f", two.TokensPerSecond, base.TokensPerSecond)
	}
	// 32B Q4 ≈ 57.3 мс + 10 мс = 67.3 мс → ~14.9 tok/s
	if two.TokensPerSecond < 13 || two.TokensPerSecond > 17 {
		t.Fatalf("32B over 10ms link = %.2f tok/s, want ~15", two.TokensPerSecond)
	}
}

func TestEstimateLargeModelAcrossWANMatchesPetals(t *testing.T) {
	// Petals: 70B через интернет — 4-6 tok/s. Проверяем порядок.
	got, _ := EstimateSpeed(Params{ModelBillions: 70, Quantization: "Q4", Stages: Homogeneous(4, 0), RTTMillis: 40})
	if got.TokensPerSecond < 3 || got.TokensPerSecond > 7 {
		t.Fatalf("70B across WAN = %.2f tok/s, want 3-7 (Petals reports 4-6)", got.TokensPerSecond)
	}
}

func TestEstimateTensorParallelSpeedsUp(t *testing.T) {
	pipeline, _ := EstimateSpeed(Params{ModelBillions: 32, Quantization: "Q4", Stages: Homogeneous(2, 0), RTTMillis: 1, SplitMode: "layer"})
	tensor, _ := EstimateSpeed(Params{ModelBillions: 32, Quantization: "Q4", Stages: Homogeneous(2, 0), RTTMillis: 1, SplitMode: "tensor"})

	if tensor.Class != "lan" {
		t.Fatalf("class = %s, want lan", tensor.Class)
	}
	if tensor.TokensPerSecond <= pipeline.TokensPerSecond {
		t.Fatalf("tensor parallel on fast link must be faster: %.2f vs %.2f",
			tensor.TokensPerSecond, pipeline.TokensPerSecond)
	}
}

func TestEstimateValidation(t *testing.T) {
	if _, err := EstimateSpeed(Params{ModelBillions: 0}); err == nil {
		t.Fatal("expected error for zero model size")
	}
	// Отрицательный RTT обрезается до нуля, отрицательные стадии — до одной.
	got, err := EstimateSpeed(Params{ModelBillions: 8, Stages: Homogeneous(-3, 0), RTTMillis: -5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Hops != 0 {
		t.Fatalf("hops = %d, want 0", got.Hops)
	}
}

func TestQuantizationFactors(t *testing.T) {
	q4, _ := EstimateSpeed(Params{ModelBillions: 8, Quantization: "Q4"})
	q8, _ := EstimateSpeed(Params{ModelBillions: 8, Quantization: "Q8_0"})
	if q8.ComputeMS <= q4.ComputeMS {
		t.Fatalf("Q8 must be slower than Q4: %.1f vs %.1f", q8.ComputeMS, q4.ComputeMS)
	}
}

func TestEstimateUsesPerNodeCalibration(t *testing.T) {
	// Быстрый узел (0.5 мс/млрд) и медленный (4 мс/млрд) на равных долях.
	params := Params{
		ModelBillions: 32,
		Quantization:  "Q4",
		Stages: []StagePerf{
			{LayerShare: 0.5, MSPerBillion: 0.5},
			{LayerShare: 0.5, MSPerBillion: 4.0},
		},
		RTTMillis: 10,
	}
	got, err := EstimateSpeed(params)
	if err != nil {
		t.Fatalf("EstimateSpeed: %v", err)
	}
	// 32 * 0.5 * 0.5 = 8 мс + 32 * 0.5 * 4 = 64 мс → 72 мс вычислений + 10 мс сети
	if got.ComputeMS < 70 || got.ComputeMS > 74 {
		t.Fatalf("compute ms = %.1f, want ~72", got.ComputeMS)
	}
	if got.NetworkMS != 10 {
		t.Fatalf("network ms = %.1f, want 10", got.NetworkMS)
	}

	// Одинаковые узлы с хорошей калибровкой должны быть быстрее дефолтной константы.
	fast, _ := EstimateSpeed(Params{ModelBillions: 32, Quantization: "Q4", Stages: Homogeneous(2, 0.9)})
	def, _ := EstimateSpeed(Params{ModelBillions: 32, Quantization: "Q4", Stages: Homogeneous(2, 0)})
	if fast.ComputeMS >= def.ComputeMS {
		t.Fatalf("calibrated node must beat default constant: %.1f vs %.1f", fast.ComputeMS, def.ComputeMS)
	}
}
