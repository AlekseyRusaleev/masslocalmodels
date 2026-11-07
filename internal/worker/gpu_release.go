package worker

import "strings"

// holdsGPU — процесс, который эта программа сама сажает на видеокарту.
//
// Чужой llama-server, запущенный не из Ollama и не из каталога нашей сборки,
// не трогаем: у человека может быть свой.
func holdsGPU(path string) bool {
	p := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(path), "/", `\`))
	if p == "" {
		return false
	}
	base := p
	if i := strings.LastIndex(p, `\`); i >= 0 {
		base = p[i+1:]
	}
	if strings.Contains(p, `\ollama\lib\ollama\`) && strings.HasPrefix(base, "llama-server") && strings.HasSuffix(base, ".exe") {
		return true
	}
	switch base {
	case "llama-server.exe", "ggml-rpc-server.exe":
	default:
		return false
	}
	return strings.Contains(p, `\computenetwork\llama\`) || strings.Contains(p, `\llama-cpp-ifm\`)
}
