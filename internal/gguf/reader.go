// Package gguf — минимальный разбор заголовка GGUF-файла.
//
// Зачем: чтобы резать модель по слоям и выдавать узлу только его диапазон,
// платформа должна знать, какие тензоры относятся к какому слою и сколько они
// занимают. Полный загрузчик не нужен: читаем метаданные и оглавление тензоров,
// данные не трогаем.
package gguf

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Tensor — запись оглавления.
type Tensor struct {
	Name   string   `json:"name"`
	Layer  int      `json:"layer"`
	Type   uint32   `json:"type"`
	Dims   []uint64 `json:"dims"`
	Bytes  uint64   `json:"bytes"`
	Offset uint64   `json:"offset"`
}

// LayerSummary — сводка по одному слою.
type LayerSummary struct {
	Layer   int      `json:"layer"`
	Tensors int      `json:"tensors"`
	Bytes   uint64   `json:"bytes"`
	Names   []string `json:"names,omitempty"`
}

// LayerRange — предлагаемый диапазон слоёв для одной части.
type LayerRange struct {
	Part       int    `json:"part"`
	LayerStart int    `json:"layer_start"`
	LayerEnd   int    `json:"layer_end"`
	Bytes      uint64 `json:"bytes"`
}

// Info — результат разбора.
type Info struct {
	Version       uint32            `json:"version"`
	TensorCount   uint64            `json:"tensor_count"`
	MetadataKeys  int               `json:"metadata_keys"`
	Architecture  string            `json:"architecture,omitempty"`
	Alignment     uint64            `json:"alignment"`
	HeaderBytes   uint64            `json:"header_bytes"`
	DataOffset    uint64            `json:"data_offset"`
	FileSize      int64             `json:"file_size"`
	TensorsBytes  uint64            `json:"tensors_bytes"`
	Layers        []LayerSummary    `json:"layers"`
	SharedTensors []Tensor          `json:"shared_tensors"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

const (
	magic        = 0x46554747 // "GGUF"
	maxTensorDim = 8
	defaultAlign = 32
)

// ReadInfo читает заголовок и оглавление файла.
func ReadInfo(path string) (Info, error) {
	file, err := os.Open(path)
	if err != nil {
		return Info{}, fmt.Errorf("gguf: open: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return Info{}, fmt.Errorf("gguf: stat: %w", err)
	}

	counter := &countingReader{reader: bufio.NewReaderSize(file, 1<<20)}
	reader := counter.Reader()

	info := Info{FileSize: stat.Size(), Metadata: map[string]string{}, Alignment: defaultAlign}

	var magicValue uint32
	if err := binary.Read(reader, binary.LittleEndian, &magicValue); err != nil {
		return Info{}, fmt.Errorf("gguf: read magic: %w", err)
	}
	if magicValue != magic {
		return Info{}, fmt.Errorf("gguf: not a GGUF file (magic %#x)", magicValue)
	}

	if err := binary.Read(reader, binary.LittleEndian, &info.Version); err != nil {
		return Info{}, fmt.Errorf("gguf: read version: %w", err)
	}
	if info.Version < 2 {
		return Info{}, fmt.Errorf("gguf: unsupported version %d", info.Version)
	}
	if err := binary.Read(reader, binary.LittleEndian, &info.TensorCount); err != nil {
		return Info{}, fmt.Errorf("gguf: read tensor count: %w", err)
	}
	var kvCount uint64
	if err := binary.Read(reader, binary.LittleEndian, &kvCount); err != nil {
		return Info{}, fmt.Errorf("gguf: read kv count: %w", err)
	}
	info.MetadataKeys = int(kvCount)

	for i := uint64(0); i < kvCount; i++ {
		key, err := readString(reader)
		if err != nil {
			return Info{}, fmt.Errorf("gguf: read kv key: %w", err)
		}
		if err := skipValue(reader, key, &info); err != nil {
			return Info{}, fmt.Errorf("gguf: read kv %q: %w", key, err)
		}
	}

	tensors := make([]Tensor, 0, info.TensorCount)
	for i := uint64(0); i < info.TensorCount; i++ {
		tensor, err := readTensorInfo(reader)
		if err != nil {
			return Info{}, err
		}
		tensors = append(tensors, tensor)
	}

	// Данные начинаются сразу после оглавления, выровненного по general.alignment.
	info.HeaderBytes = counter.Count()
	info.DataOffset = alignUp(info.HeaderBytes, info.Alignment)

	info.Layers, info.SharedTensors = summarise(tensors)
	for _, tensor := range tensors {
		info.TensorsBytes += tensor.Bytes
	}
	return info, nil
}

// readTensorInfo читает одну запись оглавления.
func readTensorInfo(reader io.Reader) (Tensor, error) {
	name, err := readString(reader)
	if err != nil {
		return Tensor{}, fmt.Errorf("gguf: read tensor name: %w", err)
	}

	var nDims uint32
	if err := binary.Read(reader, binary.LittleEndian, &nDims); err != nil {
		return Tensor{}, fmt.Errorf("gguf: read dims count: %w", err)
	}
	if nDims > maxTensorDim {
		return Tensor{}, fmt.Errorf("gguf: tensor %q has %d dims", name, nDims)
	}
	dims := make([]uint64, nDims)
	for d := range dims {
		if err := binary.Read(reader, binary.LittleEndian, &dims[d]); err != nil {
			return Tensor{}, fmt.Errorf("gguf: read dim: %w", err)
		}
	}

	var tensorType uint32
	if err := binary.Read(reader, binary.LittleEndian, &tensorType); err != nil {
		return Tensor{}, fmt.Errorf("gguf: read tensor type: %w", err)
	}
	var offset uint64
	if err := binary.Read(reader, binary.LittleEndian, &offset); err != nil {
		return Tensor{}, fmt.Errorf("gguf: read tensor offset: %w", err)
	}

	return Tensor{
		Name:   name,
		Layer:  LayerOf(name),
		Type:   tensorType,
		Dims:   dims,
		Bytes:  TensorBytes(tensorType, dims),
		Offset: offset,
	}, nil
}

// summarise группирует тензоры по слоям.
func summarise(tensors []Tensor) ([]LayerSummary, []Tensor) {
	byLayer := map[int]*LayerSummary{}
	shared := make([]Tensor, 0, 8)

	for _, tensor := range tensors {
		if tensor.Layer < 0 {
			shared = append(shared, tensor)
			continue
		}
		summary, ok := byLayer[tensor.Layer]
		if !ok {
			summary = &LayerSummary{Layer: tensor.Layer}
			byLayer[tensor.Layer] = summary
		}
		summary.Tensors++
		summary.Bytes += tensor.Bytes
		summary.Names = append(summary.Names, tensor.Name)
	}

	layers := make([]LayerSummary, 0, len(byLayer))
	for _, summary := range byLayer {
		sort.Strings(summary.Names)
		layers = append(layers, *summary)
	}
	sort.Slice(layers, func(i, j int) bool { return layers[i].Layer < layers[j].Layer })
	return layers, shared
}

// LayerOf вытаскивает номер слоя из имени тензора вида blk.12.attn_q.weight.
func LayerOf(name string) int {
	parts := strings.Split(name, ".")
	for index, part := range parts {
		if part != "blk" || index+1 >= len(parts) {
			continue
		}
		layer, err := strconv.Atoi(parts[index+1])
		if err != nil {
			return -1
		}
		return layer
	}
	return -1
}

// TensorBytes считает размер тензора в байтах по типу и измерениям.
func TensorBytes(tensorType uint32, dims []uint64) uint64 {
	if len(dims) == 0 {
		return 0
	}
	elements := uint64(1)
	for _, dim := range dims {
		elements *= dim
	}
	blockSize, typeSize := typeLayout(tensorType)
	if blockSize == 0 {
		return 0
	}
	return elements / blockSize * typeSize
}

// typeLayout возвращает размер блока и число байт на блок для типа GGML.
func typeLayout(tensorType uint32) (uint64, uint64) {
	switch tensorType {
	case 0: // F32
		return 1, 4
	case 1: // F16
		return 1, 2
	case 2: // Q4_0
		return 32, 18
	case 3: // Q4_1
		return 32, 20
	case 6: // Q5_0
		return 32, 22
	case 7: // Q5_1
		return 32, 24
	case 8: // Q8_0
		return 32, 34
	case 9: // Q8_1
		return 32, 36
	case 10: // Q2_K
		return 256, 84
	case 11: // Q3_K
		return 256, 110
	case 12: // Q4_K
		return 256, 144
	case 13: // Q5_K
		return 256, 176
	case 14: // Q6_K
		return 256, 210
	case 15: // Q8_K
		return 256, 292
	case 16: // IQ2_XXS
		return 256, 66
	case 17: // IQ2_XS
		return 256, 74
	case 18: // IQ3_XXS
		return 256, 98
	case 19: // IQ1_S
		return 256, 50
	case 20: // IQ4_NL
		return 32, 18
	case 21: // IQ3_S
		return 256, 110
	case 22: // IQ2_S
		return 256, 82
	case 23: // IQ4_XS
		return 256, 136
	case 24: // I8
		return 1, 1
	case 25: // I16
		return 1, 2
	case 26: // I32
		return 1, 4
	case 27: // I64
		return 1, 8
	case 28: // F64
		return 1, 8
	case 30: // BF16
		return 1, 2
	default:
		return 0, 0
	}
}

// readString читает строку GGUF (uint64 длина + байты).
func readString(reader io.Reader) (string, error) {
	var length uint64
	if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
		return "", err
	}
	if length > 1<<24 {
		return "", fmt.Errorf("string too long: %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// skipValue пропускает значение метаданных, забирая нужные ключи.
func skipValue(reader io.Reader, key string, info *Info) error {
	var valueType uint32
	if err := binary.Read(reader, binary.LittleEndian, &valueType); err != nil {
		return err
	}

	switch valueType {
	case 8: // string
		value, err := readString(reader)
		if err != nil {
			return err
		}
		switch key {
		case "general.architecture":
			info.Architecture = value
		case "general.name", "general.size_label", "general.license":
			info.Metadata[key] = value
		}
		return nil
	case 9: // array
		var itemType uint32
		if err := binary.Read(reader, binary.LittleEndian, &itemType); err != nil {
			return err
		}
		var length uint64
		if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
			return err
		}
		for i := uint64(0); i < length; i++ {
			if err := skipScalar(reader, itemType); err != nil {
				return err
			}
		}
		return nil
	default:
		return skipScalar(reader, valueType)
	}
}

// skipScalar пропускает одно скалярное значение.
func skipScalar(reader io.Reader, valueType uint32) error {
	var size int
	switch valueType {
	case 0, 1, 7: // uint8, int8, bool
		size = 1
	case 2, 3: // uint16, int16
		size = 2
	case 4, 5, 6: // uint32, int32, float32
		size = 4
	case 10, 11, 12: // uint64, int64, float64
		size = 8
	case 8: // строка
		_, err := readString(reader)
		return err
	default:
		return fmt.Errorf("unknown value type %d", valueType)
	}
	buf := make([]byte, size)
	_, err := io.ReadFull(reader, buf)
	return err
}

// alignUp выравнивает значение вверх до границы.
func alignUp(value, alignment uint64) uint64 {
	if alignment == 0 {
		return value
	}
	remainder := value % alignment
	if remainder == 0 {
		return value
	}
	return value + (alignment - remainder)
}

// SuggestSplit распределяет слои по частям примерно равного размера.
func SuggestSplit(info Info, parts int) []LayerRange {
	if len(info.Layers) == 0 {
		return nil
	}
	if parts < 1 {
		parts = 1
	}
	if parts > len(info.Layers) {
		parts = len(info.Layers)
	}

	var total uint64
	for _, layer := range info.Layers {
		total += layer.Bytes
	}
	target := total / uint64(parts)
	if target == 0 {
		target = 1
	}

	out := make([]LayerRange, 0, parts)
	start := info.Layers[0].Layer
	var accumulated uint64
	for index, layer := range info.Layers {
		accumulated += layer.Bytes
		isLast := index == len(info.Layers)-1
		if (accumulated >= target && len(out) < parts-1) || isLast {
			out = append(out, LayerRange{
				Part:       len(out) + 1,
				LayerStart: start,
				LayerEnd:   layer.Layer,
				Bytes:      accumulated,
			})
			start = layer.Layer + 1
			accumulated = 0
		}
	}
	return out
}

// countingReader считает прочитанные байты, чтобы знать точное смещение данных.
type countingReader struct {
	reader io.Reader
	count  uint64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.count += uint64(n)
	return n, err
}

func (c *countingReader) Count() uint64 { return c.count }

func (c *countingReader) Reader() io.Reader { return c }
