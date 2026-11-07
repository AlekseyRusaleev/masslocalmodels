package worker

import "testing"

func TestKVCacheTypeFollowsCardSize(t *testing.T) {
	if got := kvCacheType(8 << 30); got != "q4_0" {
		t.Fatalf("карта 8 ГиБ: %s", got)
	}
	if got := kvCacheType(9000 * 1024 * 1024); got != "q4_0" {
		t.Fatalf("граница 9000 МиБ: %s", got)
	}
	if got := kvCacheType(12 << 30); got != "q8_0" {
		t.Fatalf("карта 12 ГиБ: %s", got)
	}
	if got := kvCacheType(0); got != "q8_0" {
		t.Fatalf("неизвестная карта: %s", got)
	}
}

func TestWithKVCacheEnvDoesNotReplaceAnExistingValue(t *testing.T) {
	got := withKVCacheEnv([]string{"OLLAMA_KV_CACHE_TYPE=q8_0", "PATH=/bin"}, 8<<30)
	if len(got) != 3 {
		t.Fatalf("переменных %d: %v", len(got), got)
	}
	if got[0] != "OLLAMA_KV_CACHE_TYPE=q8_0" {
		t.Fatalf("тип переписан: %s", got[0])
	}
	if got[2] != "OLLAMA_FLASH_ATTENTION=1" {
		t.Fatalf("flash attention: %s", got[2])
	}
}

func TestKVCacheArgsUseTheSameTypeForKAndV(t *testing.T) {
	got := kvCacheArgs(8 << 30)
	want := []string{"--cache-type-k", "q4_0", "--cache-type-v", "q4_0"}
	if len(got) != len(want) {
		t.Fatalf("флаги %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("флаг %d = %s, нужно %s", i, got[i], want[i])
		}
	}
}
