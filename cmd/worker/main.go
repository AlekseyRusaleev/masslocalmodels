// Программа узла сети Compute: один файл для двух ролей.
//
//   - без аргументов или с флагами (--run, --invite, --auto) — окно установщика
//     и значок в трее;
//   - с аргументами join/enroll/run/status/... — тот же агент, что раньше был
//     отдельным файлом.
//
// Так участнику передают один exe: он же ставит узел, он же держит его в трее,
// он же обслуживает задания. Отдельного «второго файла рядом» больше нет.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]

	// Команды агента выполняются в консольном режиме, флаги и отсутствие
	// аргументов — графический режим.
	if len(args) > 0 && cliCommands[args[0]] {
		enableConsole()
		runCLI(args)
		return
	}

	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(os.Stderr, "неизвестная команда: %s\n", args[0])
		usage()
		os.Exit(2)
	}

	os.Exit(runGUI())
}

// usage печатает краткую справку.
func usage() {
	fmt.Println(`compute-node — узел сети Compute

Графический режим (Windows):
  compute-node                                  окно установщика, затем трей
  compute-node --run --config <файл> --model <модель>
                                                только трей, окно скрыто
  compute-node --invite <код> --auto            подключение без ручных шагов

Команды агента:
  join      --invite <код> [--pull] [--config <файл>] [--name <имя>]
  enroll    --control <url> --token <токен> [--name <имя>]
  run       [--model <модель>] [--config <файл>] [--log <файл>]
  status    [--config <файл>]
  discover  [--config <файл>]
  benchmark [--model <модель>] [--tokens N]
  model     fetch|list
  fetch     --url <url> --out <файл>
  gguf      --file <файл> [--parts N]
  serve     [--model <модель>] [--config <файл>]
  pause, resume, setup, start, stop, update, version`)
}
