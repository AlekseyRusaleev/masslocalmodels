package worker

import (
	"context"
	"os"
	"sync"
	"time"
)

var modelFitCache struct {
	sync.Mutex
	model string
	at    time.Time
	fits  bool
}

// modelFitHeadroom — запас сверх файла весов.
//
// Измеренный кэш q8_0 при окне 16384 занимает около 1,28 ГиБ. Два гигабайта
// покрывают этот кэш и немного служебной памяти карты.
const modelFitHeadroom = 2 << 30

// ModelFitsCard — true, когда одна карта вмещает весь файл и квантованный KV-кэш.
//
// Нулевая память или нулевой файл не считаются «влезает»: исключение включается
// только по явному размеру. Сумма нескольких карт здесь не складывается.
func ModelFitsCard(vramBytes, weightBytes uint64) bool {
	if vramBytes == 0 || weightBytes == 0 {
		return false
	}
	if weightBytes > vramBytes {
		return false
	}
	return vramBytes-weightBytes >= modelFitHeadroom
}

// wholeModelFits смотрит файл этой модели и самую большую карту компьютера.
//
// Это сообщение самого участника, а не оценка плоскости управления. Пока
// файл больше карты, узел остаётся в группе слоёв.
func wholeModelFits(model string) bool {
	modelFitCache.Lock()
	defer modelFitCache.Unlock()
	if modelFitCache.model == model && time.Since(modelFitCache.at) < 30*time.Second {
		return modelFitCache.fits
	}
	fits := computeWholeModelFits(model)
	modelFitCache.model = model
	modelFitCache.at = time.Now()
	modelFitCache.fits = fits
	return fits
}

func computeWholeModelFits(model string) bool {
	path := localWeightsFile(model)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 {
		return false
	}
	snap := queryNvidia(context.Background())
	var largest uint64
	for _, gpu := range snap.GPUs {
		if gpu.VRAMBytes > 0 && uint64(gpu.VRAMBytes) > largest {
			largest = uint64(gpu.VRAMBytes)
		}
	}
	return ModelFitsCard(largest, uint64(info.Size()))
}
