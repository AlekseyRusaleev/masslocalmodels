//go:build windows

package main

import (
	"unsafe"
)

// Иконка приложения рисуется в коде: отдельный файл ресурсов и сборщик
// ресурсов не нужны, а цвета совпадают с палитрой панели — тёмный фон и
// бирюзовый акцент.

var procCreateIcon = user32.NewProc("CreateIcon")

const (
	wmSetIcon = 0x0080
	iconSmall = 0
	iconBig   = 1
)

// iconMasks строит маски XOR (цвета, BGRA снизу вверх) и AND (прозрачность)
// для CreateIcon. Возвращает два буфера.
func iconMasks(size int) ([]byte, []byte) {
	// Палитра как в панели: фон #12161a, акцент #63b6a4.
	const (
		bgR, bgG, bgB = 0x12, 0x16, 0x1a
		acR, acG, acB = 0x63, 0xb6, 0xa4
	)

	xorStride := size * 4
	xor := make([]byte, xorStride*size)
	andStride := ((size + 31) / 32) * 4
	and := make([]byte, andStride*size)

	centre := float64(size-1) / 2
	outer := centre - 0.5
	dot := centre * 0.42

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// Скруглённый квадрат: расстояние до центра по «квадратной» метрике.
			dx := absFloat(float64(x) - centre)
			dy := absFloat(float64(y) - centre)
			corner := outer * 0.55
			inside := dx <= outer && dy <= outer
			if inside && dx > outer-corner && dy > outer-corner {
				cx := dx - (outer - corner)
				cy := dy - (outer - corner)
				inside = cx*cx+cy*cy <= corner*corner
			}

			row := size - 1 - y // DIB снизу вверх
			offset := row*xorStride + x*4

			if !inside {
				// Прозрачный пиксель: AND=1 (маска вырезает), цвет нулевой.
				and[row*andStride+x/8] |= 1 << uint(7-x%8)
				continue
			}

			r, g, b := byte(bgR), byte(bgG), byte(bgB)
			dist := (float64(x)-centre)*(float64(x)-centre) + (float64(y)-centre)*(float64(y)-centre)
			if dist <= dot*dot {
				r, g, b = acR, acG, acB
			}

			xor[offset+0] = b
			xor[offset+1] = g
			xor[offset+2] = r
			xor[offset+3] = 0xff
		}
	}
	return xor, and
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// createAppIcon создаёт иконку нужного размера.
func createAppIcon(size int) uintptr {
	xor, and := iconMasks(size)
	icon, _, _ := procCreateIcon.Call(
		0,
		uintptr(size), uintptr(size),
		1, 32,
		uintptr(unsafe.Pointer(&and[0])),
		uintptr(unsafe.Pointer(&xor[0])),
	)
	return icon
}

// applyWindowIcon ставит иконку окну (в панель задач и в заголовок).
func applyWindowIcon(hwnd, big, small uintptr) {
	if big != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconBig, big)
	}
	if small != 0 {
		procSendMessage.Call(hwnd, wmSetIcon, iconSmall, small)
	}
}
