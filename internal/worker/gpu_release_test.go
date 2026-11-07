package worker

import "testing"

func TestHoldsGPUOnlyOurRuntimes(t *testing.T) {
	own := []string{
		`C:\Users\Admin\AppData\Local\Programs\Ollama\lib\ollama\llama-server.exe`,
		`C:\Users\Admin\AppData\Local\Programs\Ollama\lib\ollama\llama-server-real.exe`,
		`C:\Users\Admin\AppData\Local\ComputeNetwork\llama\llama-server.exe`,
		`C:\Users\Admin\AppData\Local\ComputeNetwork\llama\ggml-rpc-server.exe`,
		`C:\Users\Admin\llama-cpp-ifm\b12167\llama\llama-server.exe`,
	}
	for _, path := range own {
		if !holdsGPU(path) {
			t.Fatalf("свой рантайм не опознан: %s", path)
		}
	}
	foreign := []string{
		`C:\ComputeNetwork\bin\compute-node.exe`,
		`C:\tools\llama-server.exe`,
		`C:\Program Files\Ollama\ollama.exe`,
		``,
	}
	for _, path := range foreign {
		if holdsGPU(path) {
			t.Fatalf("чужой процесс принят за наш: %s", path)
		}
	}
}
