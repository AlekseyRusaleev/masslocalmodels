//go:build !linux

package main

import "log/slog"

func cmdSetup([]string, *slog.Logger) {
	fail("setup — команда для Linux. На Windows запустите программу без аргументов: она ставит то же самое сама")
}

func cmdStart([]string, *slog.Logger) {
	fail("start — команда для Linux. На Windows узел запускается из окна программы")
}

func cmdStop([]string, *slog.Logger) {
	fail("stop — команда для Linux. На Windows это кнопка «Отключиться от сети»")
}

func cmdUpdate([]string, *slog.Logger) {
	fail("update — команда для Linux. На Windows программа обновляется сама")
}
