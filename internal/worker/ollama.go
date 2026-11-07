package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// OllamaSetupURL — официальный установщик Windows. Флаги Inno Setup
// проверялись на текущем установщике: /SILENT показывает свой UAC и ставит
// без лишних окон, /NORESTART не перезагружает компьютер.
const OllamaSetupURL = "https://ollama.com/download/OllamaSetup.exe"

// ErrOllamaDeclined — человек отказался ставить Ollama. Дальше модель
// скачивать нельзя: рантайма нет.
var ErrOllamaDeclined = errors.New("установка Ollama отклонена")

// EnsureOllamaOptions — как спрашивать и куда писать прогресс.
type EnsureOllamaOptions struct {
	// Ask спрашивает, можно ли скачать установщик. Вызывается только если
	// Ollama нет на диске. nil означает отказ: без окна программу не ставим.
	Ask      func() bool
	Log      func(string)
	Progress func(percent int, status string)
}

// OllamaReady — отвечает ли локальный рантайм на 127.0.0.1:11434.
func OllamaReady(ctx context.Context) bool {
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, OllamaEndpoint+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 300
}

// EnsureOllama поднимает локальный рантайм до того, как качать модель.
//
// Порядок: уже отвечает → выходим; есть программа → запускаем и ждём;
// нет программы → спрашиваем, скачиваем официальный установщик, ставим,
// ждём порт. Отказ останавливает подключение до шага модели.
func EnsureOllama(ctx context.Context, opts EnsureOllamaOptions) error {
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			opts.Log(fmt.Sprintf(format, args...))
		}
	}
	progress := func(percent int, status string) {
		if opts.Progress != nil {
			opts.Progress(percent, status)
		}
	}

	if OllamaReady(ctx) {
		return nil
	}

	if exe := OllamaExecutable(); exe != "" {
		progress(12, "запускаю Ollama")
		logf("Ollama найдена, запускаю %s", exe)
		if err := startOllama(exe); err != nil {
			return fmt.Errorf("не удалось запустить Ollama: %w", err)
		}
		if waitOllamaReady(ctx, 45*time.Second) {
			return nil
		}
	}

	if opts.Ask == nil || !opts.Ask() {
		return ErrOllamaDeclined
	}

	progress(14, "скачиваю Ollama")
	logf("скачиваю установщик Ollama")
	setup, err := downloadOllamaSetup(ctx, func(done, total int64) {
		if total > 0 {
			progress(14, fmt.Sprintf("скачиваю Ollama: %d%%", done*100/total))
		}
	})
	if err != nil {
		return fmt.Errorf("не скачан установщик Ollama: %w", err)
	}

	progress(16, "устанавливаю Ollama")
	logf("запускаю установщик Ollama, подтвердите права если Windows спросит")
	if err := runOllamaSetup(setup); err != nil {
		return fmt.Errorf("установщик Ollama не завершился: %w", err)
	}

	exe := OllamaExecutable()
	if exe != "" {
		_ = startOllama(exe)
	}
	if !waitOllamaReady(ctx, 5*time.Minute) {
		return fmt.Errorf("Ollama установлена, но не отвечает на %s", OllamaEndpoint)
	}
	logf("Ollama отвечает")
	return nil
}

func waitOllamaReady(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		if OllamaReady(ctx) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
	return OllamaReady(ctx)
}
