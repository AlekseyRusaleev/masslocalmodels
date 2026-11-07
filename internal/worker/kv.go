package worker

import (
	"context"
	"os"
	"strings"
)

// Порог из установщика: на карте до 9000 МиБ измеренный q4_0 держит окно
// 16384 на видеокарте. На большей карте остаётся измеренный q8_0.
const smallCardVRAM = 9000 * 1024 * 1024

// kvCacheType выбирает квантование KV-кэша по размеру одной карты.
//
// Неизвестный размер получает q8_0: это измеренный тип, при котором кэш
// не уезжал в процессор. Уже заданная человеком переменная не переписывается.
func kvCacheType(vramBytes uint64) string {
	if vramBytes > 0 && vramBytes <= smallCardVRAM {
		return "q4_0"
	}
	return "q8_0"
}

// kvCacheArgs — флаги llama-server для той же квантизации на каждой стадии,
// которую поднимает этот процесс. ggml-rpc-server этих флагов не получает:
// у закреплённой сборки b12167 их нет в его справке.
func kvCacheArgs(vramBytes uint64) []string {
	kind := kvCacheType(vramBytes)
	return []string{"--cache-type-k", kind, "--cache-type-v", kind}
}

// withKVCacheEnv дописывает квантование Ollama, если человек его ещё не задал.
func withKVCacheEnv(base []string, vramBytes uint64) []string {
	kind := kvCacheType(vramBytes)
	out := make([]string, 0, len(base)+2)
	haveKV := false
	haveFA := false
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, "OLLAMA_KV_CACHE_TYPE") && strings.TrimSpace(value) != "" {
			haveKV = true
		}
		if ok && strings.EqualFold(key, "OLLAMA_FLASH_ATTENTION") && strings.TrimSpace(value) != "" {
			haveFA = true
		}
		out = append(out, item)
	}
	if !haveKV {
		out = append(out, "OLLAMA_KV_CACHE_TYPE="+kind)
	}
	if !haveFA {
		out = append(out, "OLLAMA_FLASH_ATTENTION=1")
	}
	return out
}

func ollamaServeEnv() []string {
	return withKVCacheEnv(os.Environ(), largestVRAM())
}

func largestVRAM() uint64 {
	snap := queryNvidia(context.Background())
	var largest uint64
	for _, gpu := range snap.GPUs {
		if gpu.VRAMBytes > 0 && uint64(gpu.VRAMBytes) > largest {
			largest = uint64(gpu.VRAMBytes)
		}
	}
	return largest
}
