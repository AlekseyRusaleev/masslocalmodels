package gguf

import (
	"os"
	"testing"
)

func TestLayerOf(t *testing.T) {
	cases := map[string]int{
		"blk.0.attn_q.weight":        0,
		"blk.35.ffn_down.weight":     35,
		"token_embd.weight":          -1,
		"output_norm.weight":         -1,
		"blk.notanumber.attn_q.bias": -1,
		"other.0.weight":             -1,
	}
	for name, want := range cases {
		if got := LayerOf(name); got != want {
			t.Errorf("LayerOf(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestTensorBytesQ4K(t *testing.T) {
	// Q4_K: 144 байта на 256 элементов.
	got := TensorBytes(12, []uint64{4096, 4096})
	want := uint64(4096*4096/256) * 144
	if got != want {
		t.Fatalf("TensorBytes = %d, want %d", got, want)
	}
}

func TestTensorBytesF16AndF32(t *testing.T) {
	if got := TensorBytes(1, []uint64{1024}); got != 2048 {
		t.Fatalf("F16 = %d, want 2048", got)
	}
	if got := TensorBytes(0, []uint64{1024}); got != 4096 {
		t.Fatalf("F32 = %d, want 4096", got)
	}
}

func TestSuggestSplitCoversAllLayers(t *testing.T) {
	info := Info{}
	for layer := 0; layer < 36; layer++ {
		info.Layers = append(info.Layers, LayerSummary{Layer: layer, Tensors: 9, Bytes: 100})
	}

	parts := SuggestSplit(info, 3)
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(parts))
	}
	if parts[0].LayerStart != 0 {
		t.Fatalf("first part must start at 0, got %d", parts[0].LayerStart)
	}
	if parts[len(parts)-1].LayerEnd != 35 {
		t.Fatalf("last part must end at 35, got %d", parts[len(parts)-1].LayerEnd)
	}
	for i := 1; i < len(parts); i++ {
		if parts[i].LayerStart != parts[i-1].LayerEnd+1 {
			t.Fatalf("gap between parts %d and %d", i-1, i)
		}
	}
	var total uint64
	for _, part := range parts {
		total += part.Bytes
		if part.Part != i(parts, part) {
			t.Fatalf("part index mismatch: %d", part.Part)
		}
	}
	if total != 3600 {
		t.Fatalf("total bytes = %d, want 3600", total)
	}
}

// i возвращает ожидаемый номер части.
func i(parts []LayerRange, part LayerRange) int {
	for index, item := range parts {
		if item == part {
			return index + 1
		}
	}
	return -1
}

func TestSuggestSplitClampsParts(t *testing.T) {
	info := Info{Layers: []LayerSummary{{Layer: 0, Bytes: 10}, {Layer: 1, Bytes: 10}}}
	if got := SuggestSplit(info, 5); len(got) != 2 {
		t.Fatalf("parts = %d, want 2 (clamped to layer count)", len(got))
	}
	if got := SuggestSplit(Info{}, 3); got != nil {
		t.Fatal("empty model must produce no ranges")
	}
}

// TestReadRealGGUF проверяет разбор на настоящем файле, если он есть.
func TestReadRealGGUF(t *testing.T) {
	path := os.Getenv("GGUF_TEST_FILE")
	if path == "" {
		t.Skip("GGUF_TEST_FILE не задан")
	}
	info, err := ReadInfo(path)
	if err != nil {
		t.Fatalf("ReadInfo: %v", err)
	}
	if len(info.Layers) == 0 {
		t.Fatal("no layers found")
	}
	if info.Architecture == "" {
		t.Fatal("architecture not detected")
	}
	if info.DataOffset == 0 || info.DataOffset >= uint64(info.FileSize) {
		t.Fatalf("suspicious data offset %d (file %d)", info.DataOffset, info.FileSize)
	}
	t.Logf("arch=%s layers=%d tensors=%d header=%d data_offset=%d bytes=%d",
		info.Architecture, len(info.Layers), info.TensorCount, info.HeaderBytes,
		info.DataOffset, info.TensorsBytes)
	for _, part := range SuggestSplit(info, 2) {
		t.Logf("part %d: layers %d-%d, %.2f GB",
			part.Part, part.LayerStart, part.LayerEnd, float64(part.Bytes)/1024/1024/1024)
	}
}
