package worker

import "strings"

// holdsGPU — процесс, который эта программа сама сажает на видеокарту.
//
// Чужой llama-server, запущенный не из Ollama и не из каталога нашей сборки,
// не трогаем: у человека может быть свой.
func holdsGPU(path string) bool {
	p := strings.ToLower(strings.TrimSpace(path))
	p = strings.TrimSuffix(p, " (deleted)")
	p = strings.ReplaceAll(p, "/", `\`)
	if p == "" {
		return false
	}
	base := p
	if i := strings.LastIndex(p, `\`); i >= 0 {
		base = p[i+1:]
	}
	name := strings.TrimSuffix(base, ".exe")
	if strings.Contains(p, `\lib\ollama\`) && strings.HasPrefix(name, "llama-server") {
		return true
	}
	switch name {
	case "llama-server", "ggml-rpc-server":
	default:
		return false
	}
	return strings.Contains(p, `\computenetwork\llama\`) ||
		strings.Contains(p, `\.compute-network\llama\`) ||
		strings.Contains(p, `\llama-cpp-ifm\`)
}
