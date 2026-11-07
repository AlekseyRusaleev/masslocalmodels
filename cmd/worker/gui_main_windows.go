//go:build windows

// Установщик узла: одно окно, трей и прогресс.
//
// Что делает: регистрирует узел, скачивает модель, включает автозапуск и
// записывает данные для харнеса. Человеку не нужно ничего вводить.
//
// Интерфейс — нативные элементы Win32: без внешних библиотек и без cgo, один
// exe. Закрытие окна прячет его в трей, узел при этом продолжает работать.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/private-compute/network/internal/worker"
	"github.com/private-compute/network/internal/workercfg"
)

const (
	appTitle = "Подключение к сети Compute"
	// windowClassName — имя класса окна: по нему второй экземпляр находит первый.
	windowClassName = "ComputeNetworkSetup"

	idStart  = 1005
	idAuto   = 1006
	idFolder = 1007
	idUpdate = 1008
	idStop   = 1009

	idCopyType  = 1020
	idCopyURL   = 1021
	idCopyKey   = 1022
	idCopyModel = 1023

	idStatus     = 2001
	idBar        = 2002
	idLog        = 2003
	idFieldType  = 2020
	idFieldURL   = 2021
	idFieldKey   = 2022
	idFieldModel = 2023

	wmAppTray    = 0x8000 + 1
	wmAppTick    = 0x8000 + 2
	wmAppUpdate  = 0x8000 + 4
	wmAppInstall = 0x8000 + 5
	trayID       = 1
	timerID      = 1
	menuOpen     = 3001
	menuExit     = 3002
	menuStatus   = 3003
	menuPause    = 3004
	menuResume   = 3005
	menuPanel    = 3006
)

// ---------- Win32 ----------

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")

	procRegisterClassEx    = user32.NewProc("RegisterClassExW")
	procCreateWindowEx     = user32.NewProc("CreateWindowExW")
	procDefWindowProc      = user32.NewProc("DefWindowProcW")
	procDestroyWindow      = user32.NewProc("DestroyWindow")
	procShowWindow         = user32.NewProc("ShowWindow")
	procUpdateWindow       = user32.NewProc("UpdateWindow")
	procGetMessage         = user32.NewProc("GetMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessage    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage    = user32.NewProc("PostQuitMessage")
	procSetWindowText      = user32.NewProc("SetWindowTextW")
	procGetWindowText      = user32.NewProc("GetWindowTextW")
	procEnableWindow       = user32.NewProc("EnableWindow")
	procSetTimer           = user32.NewProc("SetTimer")
	procKillTimer          = user32.NewProc("KillTimer")
	procSendMessage        = user32.NewProc("SendMessageW")
	procMessageBox         = user32.NewProc("MessageBoxW")
	procLoadIcon           = user32.NewProc("LoadIconW")
	procLoadCursor         = user32.NewProc("LoadCursorW")
	procSetFocus           = user32.NewProc("SetFocus")
	procCreatePopupMenu    = user32.NewProc("CreatePopupMenu")
	procAppendMenu         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu     = user32.NewProc("TrackPopupMenu")
	procDestroyMenu        = user32.NewProc("DestroyMenu")
	procGetCursorPos       = user32.NewProc("GetCursorPos")
	procFindWindow         = user32.NewProc("FindWindowW")
	procPostMessage        = user32.NewProc("PostMessageW")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procSetForeground      = user32.NewProc("SetForegroundWindow")
	procGetForeground      = user32.NewProc("GetForegroundWindow")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procFlashWindowEx      = user32.NewProc("FlashWindowEx")
	procAllowForeground    = user32.NewProc("AllowSetForegroundWindow")
	procBringWindowToTop   = user32.NewProc("BringWindowToTop")
	procGetWindowThread    = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput  = user32.NewProc("AttachThreadInput")
	procGetCurrentThreadId = kernel32.NewProc("GetCurrentThreadId")
	procShellNotifyIcon    = shell32.NewProc("Shell_NotifyIconW")
	procGetStockObject     = gdi32.NewProc("GetStockObject")
	procSetBkMode          = gdi32.NewProc("SetBkMode")
	procCreateSolidBrush   = gdi32.NewProc("CreateSolidBrush")
	procGetSysColor        = user32.NewProc("GetSysColor")
	procInitCommonCtrlEx   = comctl32.NewProc("InitCommonControlsEx")
	procGetModuleHandle    = kernel32.NewProc("GetModuleHandleW")
	procGetConsoleWindow   = kernel32.NewProc("GetConsoleWindow")
	procSetProcessDPIAware = user32.NewProc("SetProcessDpiAwarenessContext")
	procCreateFontW        = gdi32.NewProc("CreateFontW")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procCreateActCtx       = kernel32.NewProc("CreateActCtxW")
	procActivateActCtx     = kernel32.NewProc("ActivateActCtx")
	procDeactivateActCtx   = kernel32.NewProc("DeactivateActCtx")
	procOpenClipboard      = user32.NewProc("OpenClipboard")
	procEmptyClipboard     = user32.NewProc("EmptyClipboard")
	procSetClipboardData   = user32.NewProc("SetClipboardData")
	procCloseClipboard     = user32.NewProc("CloseClipboard")
	procGlobalAlloc        = kernel32.NewProc("GlobalAlloc")
	procGlobalLock         = kernel32.NewProc("GlobalLock")
	procGlobalUnlock       = kernel32.NewProc("GlobalUnlock")
	procGlobalFree         = kernel32.NewProc("GlobalFree")
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type point struct{ x, y int32 }

type msgStruct struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

type initCommonControlsEx struct {
	size uint32
	icc  uint32
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

const (
	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	wsExAppWindow      = 0x00040000
	wsChild            = 0x40000000
	wsTabStop          = 0x00010000
	wsBorder           = 0x00800000
	wsVScroll          = 0x00200000
	esMultiline        = 0x0004
	esAutoVScroll      = 0x0040
	esAutoHScroll      = 0x0080
	esReadOnly         = 0x0800
	bsPushButton       = 0x00000000
	bsAutoCheckBox     = 0x00000003
	cbsDropDownList    = 0x0003
	pbmSetPos          = 0x0400 + 2
	pbmSetRange32      = 0x0400 + 6
	swHide             = 0
	swShow             = 5
	swMinimize         = 6
	wmDestroy          = 0x0002
	wmClose            = 0x0010
	wmCommand          = 0x0111
	wmTimer            = 0x0113
	wmSetFont          = 0x0030
	wmCtlColorStatic   = 0x0138
	nimAdd             = 0
	nimModify          = 1
	nimDelete          = 2
	nimSetVersion      = 4
	notifyIconVersion4 = 4
	nifMessage         = 0x1
	nifIcon            = 0x2
	nifTip             = 0x4
	wmLButtonUp        = 0x0202
	wmRButtonUp        = 0x0205
	wmContextMenu      = 0x007B
	ninSelect          = 0x0400
	ninKeySelect       = 0x0401
	tpmReturnCmd       = 0x0100
	tpmRightButton     = 0x0002
	defaultGUIFont     = 17
	idcArrow           = 32512
	colorWindow        = 5
	csVRedraw          = 0x0001
	csHRedraw          = 0x0002
	iccProgressClass   = 0x20
)

func windowTitle() string {
	return appTitle + "  " + worker.AgentVersion
}

func utf16Ptr(s string) *uint16 {
	ptr, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		empty, _ := syscall.UTF16PtrFromString("")
		return empty
	}
	return ptr
}

func utf16PtrToString(ptr *uint16) string {
	if ptr == nil {
		return ""
	}
	return syscall.UTF16ToString((*[1 << 16]uint16)(unsafe.Pointer(ptr))[:])
}

// ---------- состояние между потоком работы и окном ----------

type uiState struct {
	mu           sync.Mutex
	percent      int
	status       string
	lines        []string
	busy         bool
	finished     bool
	failed       bool
	harness      string
	config       string
	harnessType  string
	harnessURL   string
	harnessModel string
	harnessKey   string
}

func (s *uiState) setHarness(baseURL, model, apiKey string) {
	s.mu.Lock()
	s.harnessType = "OpenAI Compatible"
	s.harnessURL = baseURL
	s.harnessModel = model
	s.harnessKey = apiKey
	s.mu.Unlock()
}

func (s *uiState) harnessFields() (apiType, baseURL, model, apiKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.harnessType, s.harnessURL, s.harnessModel, s.harnessKey
}

func (s *uiState) log(line string) {
	s.mu.Lock()
	s.lines = append(s.lines, line)
	if len(s.lines) > 400 {
		s.lines = s.lines[len(s.lines)-400:]
	}
	s.mu.Unlock()

	// Дублируем в файл: по нему видно, что происходило, даже если окно закрыли
	// или установщик завершился до показа результата.
	if file, err := os.OpenFile(filepath.Join(os.TempDir(), "worker-setup.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		fmt.Fprintf(file, "%s %s\n", time.Now().Format("15:04:05"), line)
		_ = file.Close()
	}
}

func (s *uiState) progress(percent int, status string) {
	s.mu.Lock()
	s.percent = percent
	s.status = status
	s.mu.Unlock()
}

func (s *uiState) snapshot() (int, string, string, bool, bool, bool, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.percent, s.status, strings.Join(s.lines, "\r\n"), s.busy, s.finished, s.failed, s.harness, s.config
}

var (
	state       = &uiState{status: "готово к подключению"}
	mainHwnd    uintptr
	trayAdded   bool
	ctx, cancel = context.WithCancel(context.Background())

	argName     string
	argModel    string
	argTaskName string
	argConfig   string
	// argRun — режим трея: окно скрыто, агент работает под присмотром.
	argRun  bool
	argAuto bool
)

// parseArgs читает аргументы командной строки.
func parseArgs() {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		switch args[i] {
		case "--name":
			argName = next
			i++
		case "--model":
			argModel = next
			i++
		case "--task-name":
			argTaskName = next
			i++
		case "--config":
			argConfig = next
			i++
		case "--run":
			argRun = true
		case "--auto":
			argAuto = true
		}
	}
}

// ---------- оформление ----------

// uiFont — шрифт всех элементов окна.
var uiFont uintptr

// hbrWindow — кисть фона окна: её же возвращаем подписям, чтобы фон был общий.
var hbrWindow uintptr

// visualStylesActive — хэндл контекста активации, чтобы не освобождать его.
var visualStylesActive uintptr

// activateVisualStyles включает современный вид элементов управления.
//
// Без манифеста приложения comctl32 отдаёт классический стиль: кнопки с
// двойной рамкой, серые «вдавленные» группы, мелкий шрифт. Манифест лежит
// отдельным файлом (ресурсов в бинарнике нет), поэтому создаём контекст
// активации через CreateActCtxW.
func activateVisualStyles() {
	manifest := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="ComputeNetwork.Node" version="1.0.0.0" processorArchitecture="*"/>
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0"
        processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2</dpiAwareness>
      <longPathAware xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">true</longPathAware>
    </windowsSettings>
  </application>
</assembly>`

	path := filepath.Join(os.TempDir(), "compute-node.manifest")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		return
	}

	// ACTCTX: размер структуры, флаги, путь к манифесту.
	type actCtx struct {
		cbSize              uint32
		dwFlags             uint32
		lpSource            *uint16
		wProcessorArch      uint16
		wLangID             uint16
		lpAssemblyDirectory *uint16
		lpResourceName      *uint16
		lpApplicationName   *uint16
		hModule             uintptr
	}

	// ACTCTX_FLAG_ASSEMBLY_DIRECTORY_VALID (0x4) — единственный флаг, который
	// нужен для манифеста из файла. Раньше здесь стоял 0x8
	// (RESOURCE_NAME_VALID), и CreateActCtx получал «manifest с ресурсом», то
	// есть недействительный контекст: тема не включалась, окно оставалось в
	// классическом стиле.
	ctx := actCtx{
		dwFlags:             0x4,
		lpSource:            utf16Ptr(path),
		lpAssemblyDirectory: utf16Ptr(os.TempDir()),
	}
	ctx.cbSize = uint32(unsafe.Sizeof(ctx))

	handle, _, _ := procCreateActCtx.Call(uintptr(unsafe.Pointer(&ctx)))
	if handle == 0 || handle == uintptr(^uintptr(0)) {
		return
	}
	var cookie uintptr
	if ok, _, _ := procActivateActCtx.Call(handle, uintptr(unsafe.Pointer(&cookie))); ok == 0 {
		return
	}
	visualStylesActive = handle

	// Теперь comctl32 грузится версией 6: регистрируем классы, иначе кнопки и
	// прогресс останутся нетематизированными.
	const (
		iccWin95    = 0x000000FF
		iccProgress = 0x00000020
		iccStandard = 0x0000FFFF
	)
	initEx := initCommonControlsEx{
		size: uint32(unsafe.Sizeof(initCommonControlsEx{})),
		icc:  iccWin95 | iccProgress | iccStandard,
	}
	procInitCommonCtrlEx.Call(uintptr(unsafe.Pointer(&initEx)))
}

// createUIFont создаёт шрифт Segoe UI 9pt — тот, что используют современные
// приложения. Без него Win32 берёт MS Shell Dlg, и окно выглядит старым.
func createUIFont() uintptr {
	const (
		defaultCharset = 1
		clearType      = 5
	)
	// 9pt при 96 DPI = -12 пикселей; отрицательная высота означает «по символу».
	// Значение берём переменной: константу -12 нельзя привести к uintptr.
	height := int32(-12)
	font, _, _ := procCreateFontW.Call(
		uintptr(uint32(height)), 0, 0, 0,
		400, // FW_NORMAL
		0, 0, 0,
		uintptr(defaultCharset),
		0, 0, uintptr(clearType), 0,
		uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))),
	)
	return font
}

// headingFont — шрифт заголовка: тот же Segoe UI, но крупнее и полужирный.
var headingFont uintptr

// heading создаёт подпись-заголовок: крупнее обычного текста, полужирная.
func heading(parent uintptr, text string, x, y, w, h int32) uintptr {
	hwnd := create(parent, "STATIC", text, wsChild|wsVisible, x, y, w, h, 0)
	if headingFont != 0 {
		procSendMessage.Call(hwnd, wmSetFont, headingFont, 1)
	}
	return hwnd
}

// createHeadingFont — шрифт заголовка 13pt полужирный.
func createHeadingFont() uintptr {
	const (
		weightSemibold   = 600
		qualityCleartype = 5
	)
	height := int32(-17) // 13pt при 96 DPI
	font, _, _ := procCreateFontW.Call(
		uintptr(uint32(height)), 0, 0, 0,
		uintptr(weightSemibold),
		0, 0, 0,
		1, // DEFAULT_CHARSET
		0, 0, uintptr(qualityCleartype), 0,
		uintptr(unsafe.Pointer(utf16Ptr("Segoe UI"))),
	)
	return font
}

// ---------- элементы интерфейса ----------

func create(parent uintptr, class, text string, style uint32, x, y, w, h int32, id uintptr) uintptr {
	hwnd, _, _ := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(utf16Ptr(class))),
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, id, 0, 0,
	)
	font := uiFont
	if font == 0 {
		font, _, _ = procGetStockObject.Call(uintptr(defaultGUIFont))
	}
	procSendMessage.Call(hwnd, wmSetFont, font, 1)
	return hwnd
}

func label(parent uintptr, text string, x, y, w, h int32) uintptr {
	return create(parent, "STATIC", text, wsChild|wsVisible, x, y, w, h, 0)
}

func createHarnessRow(parent uintptr, caption string, y int32, fieldID, copyID uintptr) uintptr {
	label(parent, caption, 24, y+4, 100, 18)
	field := create(parent, "EDIT", "", wsChild|wsVisible|wsBorder|wsTabStop|esReadOnly|esAutoHScroll, 128, y, 620, 24, fieldID)
	btn := create(parent, "BUTTON", "Скопировать", wsChild|wsVisible|bsPushButton|wsTabStop, 756, y-2, 142, 28, copyID)
	procEnableWindow.Call(btn, 0)
	return field
}

func setFieldText(hwnd uintptr, text string) {
	if hwnd == 0 {
		return
	}
	procSetWindowText.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr(text))))
}

func tryLoadConnectionNote() {
	path := filepath.Join(filepath.Dir(defaultConfigPath()), "connection.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var base, model, key string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case strings.HasPrefix(line, "Базовый URL:"):
			base = strings.TrimSpace(strings.TrimPrefix(line, "Базовый URL:"))
		case strings.HasPrefix(line, "Модель:") && model == "":
			model = strings.TrimSpace(strings.TrimPrefix(line, "Модель:"))
		case strings.HasPrefix(line, "Ключ API:") && key == "":
			key = strings.TrimSpace(strings.TrimPrefix(line, "Ключ API:"))
		}
	}
	if base != "" && model != "" && key != "" {
		state.setHarness(base, model, key)
	}
}

func copyToClipboard(hwnd uintptr, text string) bool {
	if text == "" {
		return false
	}
	u16, err := syscall.UTF16FromString(text)
	if err != nil {
		return false
	}
	mem, _, _ := procGlobalAlloc.Call(0x0002, uintptr(len(u16)*2)) // GMEM_MOVEABLE
	if mem == 0 {
		return false
	}
	ptr, _, _ := procGlobalLock.Call(mem)
	if ptr == 0 {
		procGlobalFree.Call(mem)
		return false
	}
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(ptr)), len(u16)), u16)
	procGlobalUnlock.Call(mem)
	opened, _, _ := procOpenClipboard.Call(hwnd)
	if opened == 0 {
		procGlobalFree.Call(mem)
		return false
	}
	procEmptyClipboard.Call()
	ok, _, _ := procSetClipboardData.Call(13, mem) // CF_UNICODETEXT
	procCloseClipboard.Call()
	if ok == 0 {
		procGlobalFree.Call(mem)
		return false
	}
	return true
}

func copyHarness(hwnd uintptr, which uintptr) {
	apiType, baseURL, model, apiKey := state.harnessFields()
	value, label := "", ""
	switch which {
	case idCopyType:
		value, label = apiType, "тип API"
	case idCopyURL:
		value, label = baseURL, "Base URL"
	case idCopyKey:
		value, label = apiKey, "ключ API"
	case idCopyModel:
		value, label = model, "модель"
	}
	if !copyToClipboard(hwnd, value) {
		return
	}
	state.mu.Lock()
	if !state.busy {
		state.status = "скопировано: " + label
	}
	state.mu.Unlock()
}

func windowText(hwnd uintptr) string {
	buf := make([]uint16, 4096)
	procGetWindowText.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

// ---------- трей ----------

func addTray(hwnd uintptr) {
	icon := appIconSmall
	if icon == 0 {
		icon, _, _ = procLoadIcon.Call(0, 32512) // запасной вариант: системная
	}
	var data notifyIconData
	data.cbSize = uint32(unsafe.Sizeof(data))
	data.hWnd = hwnd
	data.uID = trayID
	data.uFlags = nifMessage | nifIcon | nifTip
	data.uCallbackMessage = wmAppTray
	data.hIcon = icon
	copy(data.szTip[:], syscall.StringToUTF16(trayTipText()))
	result, _, _ := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&data)))
	trayAdded = result != 0

	// Версия 4 включает современное поведение: клик приходит как NIN_SELECT,
	// а не только как WM_LBUTTONUP, и «призрачные» значки не остаются.
	version := notifyIconVersion4
	procShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&data)))
	_ = version
}

// trayTipText собирает подсказку по фактическому состоянию.
func trayTipText() string {
	switch {
	case agentRunning() && nodePaused():
		return "Compute Network: узел на паузе"
	case agentRunning():
		return "Compute Network: узел работает"
	case stateIsFinished():
		return "Compute Network: узел подключён, агент остановлен"
	default:
		return "Compute Network: ждём приглашение"
	}
}

func updateTrayTip(hwnd uintptr, tip string) {
	if !trayAdded {
		return
	}
	icon := appIconSmall
	if icon == 0 {
		icon, _, _ = procLoadIcon.Call(0, 32512)
	}
	var data notifyIconData
	data.cbSize = uint32(unsafe.Sizeof(data))
	data.hWnd = hwnd
	data.uID = trayID
	data.uFlags = nifTip | nifIcon
	data.hIcon = icon
	copy(data.szTip[:], syscall.StringToUTF16(tip))
	// NIM_MODIFY, а не NIM_ADD: иначе Windows не обновляет текст подсказки.
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

func removeTray(hwnd uintptr) {
	if !trayAdded {
		return
	}
	var data notifyIconData
	data.cbSize = uint32(unsafe.Sizeof(data))
	data.hWnd = hwnd
	data.uID = trayID
	procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
	trayAdded = false
}

// nodeConfigPath — файл, который читает и окно, и агент.
func nodeConfigPath() string {
	state.mu.Lock()
	config := state.config
	state.mu.Unlock()
	if config != "" {
		return config
	}
	if argConfig != "" {
		return argConfig
	}
	return defaultConfigPath()
}

// nodePaused сообщает, стоит ли узел на паузе (по конфигурации).
func nodePaused() bool {
	cfg, err := workercfg.Load(nodeConfigPath())
	if err != nil {
		return false
	}
	return cfg.Paused
}

// setNodePaused меняет паузу в конфигурации: агент читает её на каждом шаге.
func setNodePaused(paused bool) bool {
	path := nodeConfigPath()
	cfg, err := workercfg.Load(path)
	if err != nil {
		state.log("не удалось прочитать конфигурацию: " + err.Error())
		return false
	}
	cfg.Paused = paused
	if err := workercfg.Save(path, cfg); err != nil {
		state.log("не удалось сохранить состояние: " + err.Error())
		return false
	}
	return true
}

// showTrayMenu показывает меню по правому клику на значке.
func showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	procAppendMenu.Call(menu, 0, menuOpen, uintptr(unsafe.Pointer(utf16Ptr("Открыть окно"))))
	procAppendMenu.Call(menu, 0, menuPanel, uintptr(unsafe.Pointer(utf16Ptr("Открыть панель в браузере"))))
	if nodePaused() {
		procAppendMenu.Call(menu, 0, menuResume, uintptr(unsafe.Pointer(utf16Ptr("Возобновить работу узла"))))
	} else {
		procAppendMenu.Call(menu, 0, menuPause, uintptr(unsafe.Pointer(utf16Ptr("Пауза (не брать задания)"))))
	}
	procAppendMenu.Call(menu, 0, menuStatus, uintptr(unsafe.Pointer(utf16Ptr("Проверить состояние"))))
	procAppendMenu.Call(menu, 0x800, 0, 0) // MF_SEPARATOR
	procAppendMenu.Call(menu, 0, menuExit, uintptr(unsafe.Pointer(utf16Ptr("Выход"))))

	var cursor point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
	procSetForeground.Call(hwnd)
	command, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton,
		uintptr(cursor.x), uintptr(cursor.y), 0, hwnd, 0)
	procDestroyMenu.Call(menu)

	switch command {
	case menuOpen:
		procShowWindow.Call(hwnd, swShow)
		procSetForeground.Call(hwnd)
	case menuStatus:
		percent, status, _, busy, finished, failed, _, config := state.snapshot()
		text := fmt.Sprintf("%s %d%%", status, percent)
		switch {
		case busy:
			text = strings.Join([]string{status, fmt.Sprintf("прогресс: %d%%", percent)}, "\n")
		case finished:
			text = "узел подключён и обслуживает модель"
			if config != "" {
				text += "\nконфигурация: " + config
			}
		case failed:
			text = "подключение не завершено: " + status
		}
		procMessageBox.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr(text))),
			uintptr(unsafe.Pointer(utf16Ptr("Состояние узла"))), 0x40)
	case menuPause:
		if setNodePaused(true) {
			state.mu.Lock()
			if !state.busy {
				state.status = "узел на паузе"
			}
			state.mu.Unlock()
			state.log("пауза: новые задания не берутся")
		}
	case menuResume:
		if setNodePaused(false) {
			state.mu.Lock()
			if !state.busy {
				state.status = "узел подключён"
			}
			state.mu.Unlock()
			state.log("узел снова берёт задания")
		}
	case menuExit:
		ctxCancel()
		procDestroyWindow.Call(hwnd)
	}
}

// ---------- рабочий поток ----------

func ctxCancel() { cancel() }

func startJoin(hwnd uintptr) {
	joinMu.Lock()
	if joinCancel != nil {
		joinMu.Unlock()
		return
	}
	joinMu.Unlock()
	// Узел уже получал ключ. Повторное подключение не регистрирует машину заново:
	// иначе каждая кнопка выпускает новый ключ и нового участника.
	if resumeExistingNode(hwnd) {
		return
	}
	joinMu.Lock()
	if joinCancel != nil {
		joinMu.Unlock()
		return
	}
	joinCtx, cancel := context.WithCancel(ctx)
	joinCancel = cancel
	joinMu.Unlock()

	askOllama := func() bool { return true }
	if !worker.OllamaReady(ctx) && worker.OllamaExecutable() == "" {
		if !askOllamaInstall(hwnd) {
			joinMu.Lock()
			joinCancel = nil
			joinMu.Unlock()
			cancel()
			state.progress(0, "установка Ollama отклонена")
			state.log("без Ollama модель не скачается")
			procEnableWindow.Call(getControl(hwnd, idStart), 1)
			return
		}
	}

	state.mu.Lock()
	state.busy = true
	state.finished = false
	state.failed = false
	state.lines = nil
	state.percent = 0
	state.status = "начали"
	state.mu.Unlock()

	enableControls(hwnd, false)

	cfgPath := defaultConfigPath()
	harnessPath := filepath.Join(filepath.Dir(cfgPath), "harness-settings.yaml")

	go func() {
		defer func() {
			joinMu.Lock()
			joinCancel = nil
			joinMu.Unlock()
		}()
		selfResult, selfErr := worker.SelfRegister(joinCtx, worker.SelfRegisterOptions{
			ControlURL:  "",
			Name:        argName,
			ConfigPath:  cfgPath,
			HarnessPath: harnessPath,
			PullModel:   true,
			AskOllama:   askOllama,
			Log:         state.log,
			Progress:    state.progress,
			OnCredentials: func(baseURL, model, apiKey string) {
				state.setHarness(baseURL, model, apiKey)
			},
		})
		if joinCtx.Err() != nil {
			state.mu.Lock()
			state.busy = false
			state.mu.Unlock()
			enableControls(hwnd, true)
			return
		}
		var finishLines []string
		state.mu.Lock()
		state.busy = false
		if selfErr != nil {
			state.failed = true
			state.status = "ошибка: " + selfErr.Error()
			finishLines = append(finishLines, "ОШИБКА: "+selfErr.Error())
		} else {
			state.finished = true
			state.status = "узел подключён"
			state.config = cfgPath
			if selfResult.Model == "" {
				finishLines = append(finishLines, "готово: узел в сети, долю весов пришлёт платформа")
			} else {
				finishLines = append(finishLines, "готово: узел обслуживает модель "+selfResult.Model)
			}
		}
		state.mu.Unlock()
		for _, line := range finishLines {
			state.log(line)
		}
		if selfErr == nil {
			if err := startAgent(cfgPath, selfResult.Config.Model); err != nil {
				state.log(err.Error())
			}
		}
		enableControls(hwnd, true)
	}()
}

func askOllamaInstall(hwnd uintptr) bool {
	const mbYesNo = 0x00000004
	const mbIconQuestion = 0x00000020
	const idYes = 6
	text := "Ollama на этом компьютере не найдена. Скачать официальный установщик и поставить её сейчас? Без неё модель не загрузится."
	r, _, _ := procMessageBox.Call(
		hwnd,
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(unsafe.Pointer(utf16Ptr("Ollama"))),
		mbYesNo|mbIconQuestion,
	)
	return r == idYes
}

// resumeExistingNode поднимает уже зарегистрированный узел тем же ключом.
// Возвращает false только когда регистрации ещё не было.
func resumeExistingNode(hwnd uintptr) bool {
	cfgPath := defaultConfigPath()
	cfg, err := workercfg.Load(cfgPath)
	if err != nil || cfg.DeviceToken == "" {
		return false
	}
	cfg.Paused = false
	if err := workercfg.Save(cfgPath, cfg); err != nil {
		state.log("не удалось вернуть узел в сеть: " + err.Error())
		return true
	}
	tryLoadConnectionNote()
	state.mu.Lock()
	state.config = cfgPath
	state.busy = false
	state.finished = true
	state.failed = false
	state.status = "узел подключён"
	state.mu.Unlock()
	state.log("узел уже зарегистрирован, ключ API не меняется")
	go func() {
		if _, err := worker.EnsureLlama(context.Background(), worker.EnsureLlamaOptions{
			Log:      state.log,
			Progress: state.progress,
		}); err != nil {
			state.log("llama.cpp не установлена: " + err.Error())
		}
	}()
	if err := startAgent(cfgPath, cfg.Model); err != nil {
		state.log(err.Error())
	}
	enableControls(hwnd, true)
	return true
}

func disconnectNode(hwnd uintptr) {
	joinMu.Lock()
	cancel := joinCancel
	joinMu.Unlock()
	if cancel != nil {
		cancel()
	}
	stopAgent()
	cfgPath := defaultConfigPath()
	if cfg, err := workercfg.Load(cfgPath); err == nil {
		cfg.Paused = true
		_ = workercfg.Save(cfgPath, cfg)
	}
	state.mu.Lock()
	state.busy = false
	state.finished = false
	state.failed = false
	state.status = "узел отключён"
	state.mu.Unlock()
	state.log("узел отключён от сети")
	enableControls(hwnd, true)
}

var (
	joinMu     sync.Mutex
	joinCancel context.CancelFunc
)

// io_discard глушит журнал агента: всё важное идёт в интерфейс.
type io_discard struct{}

func (io_discard) Write(p []byte) (int, error) { return len(p), nil }

func getControl(hwnd uintptr, id int) uintptr {
	child, _, _ := user32.NewProc("GetDlgItem").Call(hwnd, uintptr(id))
	return child
}

func enableControls(hwnd uintptr, enabled bool) {
	flag := uintptr(0)
	if enabled {
		flag = 1
	}
	procEnableWindow.Call(getControl(hwnd, idStart), flag)
	procEnableWindow.Call(getControl(hwnd, idStop), 1)
	procEnableWindow.Call(getControl(hwnd, idAuto), boolFlag(enabled && stateIsFinished()))
	procEnableWindow.Call(getControl(hwnd, idFolder), boolFlag(stateIsFinished()))
}

func boolFlag(value bool) uintptr {
	if value {
		return 1
	}
	return 0
}

func stateIsFinished() bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.finished
}

// defaultConfigPath выбирает каталог, куда установщик реально может писать.
//
// Сначала пробуем ProgramData (общий каталог), но он доступен только
// администратору: у обычного пользователя проверяем запись и отступаем в
// LOCALAPPDATA. Молча выбрать недоступный путь нельзя — иначе регистрация
// пройдёт, а конфигурация не сохранится.
func defaultConfigPath() string {
	candidates := make([]string, 0, 2)
	if programData := os.Getenv("ProgramData"); programData != "" {
		candidates = append(candidates, filepath.Join(programData, "ComputeNetwork"))
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "ComputeNetwork"))
	}

	for _, dir := range candidates {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			continue
		}
		probe := filepath.Join(dir, ".write-probe")
		file, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			continue
		}
		_ = file.Close()
		_ = os.Remove(probe)
		return filepath.Join(dir, "agent.json")
	}

	return filepath.Join(os.Getenv("LOCALAPPDATA"), "ComputeNetwork", "agent.json")
}

// installAutostart включает автозапуск в фоновом потоке.
//
// Работа с планировщиком занимает секунды (запускается PowerShell), поэтому
// выполнять её в потоке окна нельзя: интерфейс замирает и выглядит зависшим.
// Результат показываем в журнале и строке состояния, без модальных окон.
func installAutostart(hwnd uintptr) {
	state.mu.Lock()
	config := state.config
	state.mu.Unlock()
	if config == "" {
		state.progress(0, "сначала подключите узел")
		state.log("автозапуск: узел ещё не подключён")
		return
	}

	state.progress(0, "включаю автозапуск")
	state.log("включаю автозапуск…")

	go func() {
		exe, err := os.Executable()
		if err != nil {
			state.log("автозапуск: не удалось определить путь к программе: " + err.Error())
			return
		}
		cfg, err := workercfg.Load(config)
		if err != nil {
			state.log("автозапуск: конфигурация не прочитана: " + err.Error())
			return
		}

		// В автозапуск идёт это приложение: оно стартует в трее и само поднимает
		// агент без консольного окна.
		if err := worker.RegisterSupervisorAutostart(exe, config, cfg.Model, argTaskName); err != nil {
			state.log("автозапуск: " + err.Error())
			return
		}
		state.log("автозапуск включён: узел будет подниматься при входе в систему")
		state.progress(100, "автозапуск включён")
	}()
}

// ---------- окно ----------

func run() int {
	procSetProcessDPIAware.Call(0xFFFFFFFFFFFFFFF8) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4

	initEx := initCommonControlsEx{size: uint32(unsafe.Sizeof(initCommonControlsEx{})), icc: iccProgressClass}
	procInitCommonCtrlEx.Call(uintptr(unsafe.Pointer(&initEx)))

	hInstance, _, _ := procGetModuleHandle.Call(0)
	className := utf16Ptr(windowClassName)

	var wc wndClassEx
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	wc.style = csHRedraw | csVRedraw
	wc.lpfnWndProc = syscall.NewCallback(wndProc)
	wc.hInstance = hInstance
	wc.hCursor, _, _ = procLoadCursor.Call(0, idcArrow)
	// Одна кисть на окно: её же возвращаем подписям, чтобы фон был общий.
	// GetSysColor переводит индекс системного цвета в COLORREF, иначе в кисть
	// уходит номер цвета (5) — почти чёрный фон вместо белого.
	windowColor, _, _ := procGetSysColor.Call(uintptr(colorWindow))
	hbrWindow, _, _ = procCreateSolidBrush.Call(windowColor)
	wc.hbrBackground = hbrWindow

	wc.lpszClassName = className
	procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))

	style := uint32(wsOverlappedWindow)
	if !argRun {
		// Окно видимо сразу, ещё до ShowWindow. Иначе скрытый установщик
		// обновления передаёт «не показывать», и на панели задач пусто.
		style |= wsVisible
	}
	hwnd, _, _ := procCreateWindowEx.Call(
		wsExAppWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(utf16Ptr(windowTitle()))),
		uintptr(style),
		140, 100, 960, 700,
		0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return 1
	}
	mainHwnd = hwnd

	// В режиме трея окно не показываем вовсе: пользователь видит только значок,
	// а консольного окна нет ни в каком виде.
	if !argRun {
		// Первый ShowWindow Windows подменяет значением из запуска процесса.
		// Скрытый установщик обновления передаёт «не показывать», и окно так и
		// остаётся невидимым, хотя программа уже работает. Второй вызов
		// показывается по-настоящему.
		showMainWindow(hwnd)
		procUpdateWindow.Call(hwnd)
	}
	appIconBig = createAppIcon(32)
	appIconSmall = createAppIcon(16)
	applyWindowIcon(hwnd, appIconBig, appIconSmall)
	addTray(hwnd)
	procSetTimer.Call(hwnd, timerID, 250, 0)

	var message msgStruct
	for {
		ret, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}

	removeTray(hwnd)
	return 0
}

func onCreate(hwnd uintptr) {
	heading(hwnd, "Подключение компьютера к сети Compute", 24, 16, 620, 28)
	label(hwnd, "версия "+worker.AgentVersion, 660, 22, 250, 20)

	create(hwnd, "BUTTON", "Подключиться к сети", wsChild|wsVisible|bsPushButton|wsTabStop, 24, 52, 220, 32, idStart)
	create(hwnd, "BUTTON", "Отключиться от сети", wsChild|wsVisible|bsPushButton|wsTabStop, 252, 52, 230, 32, idStop)
	create(hwnd, "BUTTON", "Включить автозапуск", wsChild|wsVisible|bsPushButton|wsTabStop, 490, 52, 200, 32, idAuto)
	create(hwnd, "BUTTON", "Открыть папку узла", wsChild|wsVisible|bsPushButton|wsTabStop, 698, 52, 200, 32, idFolder)

	label(hwnd, "Состояние", 24, 96, 220, 18)
	label(hwnd, "Объем весов", 280, 96, 220, 18)
	stateStatus = create(hwnd, "STATIC", "готово к подключению", wsChild|wsVisible, 24, 114, 240, 20, 0)
	weightStatus = create(hwnd, "STATIC", "0 Б", wsChild|wsVisible, 280, 114, 240, 20, 0)
	updateButton = create(hwnd, "BUTTON", "Обновить программу", wsChild|bsPushButton|wsTabStop, 698, 108, 200, 28, idUpdate)

	stateBar = create(hwnd, "msctls_progress32", "", wsChild|wsVisible, 24, 136, 874, 18, idBar)
	procSendMessage.Call(stateBar, pbmSetRange32, 0, 100)

	label(hwnd, "Для харнеса", 24, 164, 200, 18)
	fieldType = createHarnessRow(hwnd, "Тип API", 186, idFieldType, idCopyType)
	fieldURL = createHarnessRow(hwnd, "Base URL", 218, idFieldURL, idCopyURL)
	fieldKey = createHarnessRow(hwnd, "Ключ API", 250, idFieldKey, idCopyKey)
	fieldModel = createHarnessRow(hwnd, "Модель", 282, idFieldModel, idCopyModel)
	tryLoadConnectionNote()

	label(hwnd, "Журнал", 24, 320, 200, 18)
	procCreateWindowEx.Call(
		0x00000200, // WS_EX_CLIENTEDGE
		uintptr(unsafe.Pointer(utf16Ptr("EDIT"))),
		0,
		uintptr(wsChild|wsVisible|wsBorder|wsVScroll|esMultiline|esReadOnly|esAutoVScroll),
		24, 338, 874, 246,
		hwnd, idLog, 0, 0,
	)
	logBox = getControl(hwnd, idLog)
	go watchUpdates()
	if !argRun {
		resumeIfEnrolled()
	}

	// Режим трея (автозапуск): окно сразу скрыто, агент работает под присмотром.
	if argRun {
		if argConfig == "" {
			argConfig = defaultConfigPath()
		}
		state.mu.Lock()
		state.config = argConfig
		state.finished = true
		state.status = "узел работает в трее"
		state.mu.Unlock()
		procShowWindow.Call(hwnd, swHide)

		go func() {
			cfg, err := workercfg.Load(argConfig)
			if err != nil {
				state.log("узел не подключён: " + err.Error())
				return
			}
			model := argModel
			if model == "" {
				model = cfg.Model
			}
			state.log(fmt.Sprintf("окно контекста: %d токенов", worker.NumCtxOf(cfg)))
			if err := startAgent(argConfig, model); err != nil {
				state.log(err.Error())
			}
		}()
	}

	if argAuto {
		go func() { time.Sleep(600 * time.Millisecond); startJoin(hwnd) }()
	}
}

var (
	// Иконки приложения: рисуются в коде, ресурсный файл не нужен.
	appIconBig   uintptr
	appIconSmall uintptr

	stateStatus  uintptr
	weightStatus uintptr
	stateBar     uintptr
	logBox       uintptr
	fieldType    uintptr
	fieldURL     uintptr
	fieldKey     uintptr
	fieldModel   uintptr
	updateButton uintptr
)

func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmCtlColorStatic:
		// Подписи не должны рисовать себе серый прямоугольник поверх окна:
		// просим прозрачный фон и отдаём кисть окна.
		procSetBkMode.Call(wParam, 1) // TRANSPARENT
		return hbrWindow

	case 0x0001: // WM_CREATE
		onCreate(hwnd)
		return 0

	case wmCommand:
		switch uint32(wParam & 0xFFFF) {
		case idStart:
			startJoin(hwnd)
		case idStop:
			disconnectNode(hwnd)
		case idAuto:
			installAutostart(hwnd)
		case idFolder:
			openFolder(hwnd)
		case idUpdate:
			startUpdate(hwnd)
		case idCopyType, idCopyURL, idCopyKey, idCopyModel:
			copyHarness(hwnd, wParam&0xFFFF)
		}
		return 0

	case wmAppTray:
		// Версия 4 присылает NIN_SELECT вместо WM_LBUTTONUP, а контекстное меню —
		// как WM_CONTEXTMENU. Поддерживаем оба протокола: поведение значка не
		// должно зависеть от версии оболочки.
		switch uint32(lParam & 0xFFFF) {
		case wmLButtonUp, ninSelect, ninKeySelect:
			showMainWindow(hwnd)
		case wmRButtonUp, wmContextMenu:
			showTrayMenu(hwnd)
		}
		return 0

	case wmTimer:
		revealAfterUpdate(hwnd)
		refreshUI()
		return 0

	case wmAppShow:
		showMainWindow(hwnd)
		return 0

	case wmAppUpdate:
		// Пока этот процесс ещё может передать передний план, разрешаем его
		// новому окну. Иначе после обновления программа есть, но её не видно.
		procAllowForeground.Call(^uintptr(0))
		stopAgent()
		ctxCancel()
		procDestroyWindow.Call(hwnd)
		return 0

	case wmAppInstall:
		startUpdate(hwnd)
		return 0

	case wmClose:
		// Свернуть в трей: узел продолжает работу.
		procShowWindow.Call(hwnd, swHide)
		updateTrayTip(hwnd, "Compute Network: окно свёрнуто, узел работает")
		return 0

	case wmDestroy:
		procKillTimer.Call(hwnd, timerID)
		procPostQuitMessage.Call(0)
		return 0
	}

	result, _, _ := procDefWindowProc.Call(hwnd, message, wParam, lParam)
	return result
}

var (
	revealTries int
	revealDone  bool

	updateMu    sync.Mutex
	updateOffer worker.Release
	updateKnown bool
	updateBusy  bool
	shownUpdate string
)

// watchUpdates спрашивает платформу при открытии и затем раз в минуту.
// Кнопка появляется только когда на сервере лежит сборка новее запущенной.
func watchUpdates() {
	checkUpdate()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkUpdate()
		}
	}
}

func checkUpdate() {
	updateMu.Lock()
	busy := updateBusy
	updateMu.Unlock()
	if busy {
		return
	}
	rel, newer, err := worker.CheckRelease(ctx, "")
	if err != nil || !newer {
		return
	}
	updateMu.Lock()
	updateOffer = rel
	updateKnown = true
	busy = updateBusy
	updateMu.Unlock()
	if !busy && mainHwnd != 0 {
		procPostMessage.Call(mainHwnd, wmAppInstall, 0, 0)
	}
}

// resumeIfEnrolled поднимает уже зарегистрированный узел без кнопки.
// После тихого обновления программа сама возвращается в сеть. Пауза и
// отключение сохраняются: их человек включил специально.
func resumeIfEnrolled() {
	path := nodeConfigPath()
	cfg, err := workercfg.Load(path)
	if err != nil || cfg.DeviceToken == "" || cfg.Paused {
		return
	}
	state.mu.Lock()
	state.config = path
	state.finished = true
	state.status = "узел подключён"
	state.mu.Unlock()
	if err := startAgent(path, cfg.Model); err != nil {
		state.log(err.Error())
	}
}

func startUpdate(hwnd uintptr) {
	updateMu.Lock()
	if !updateKnown || updateBusy {
		updateMu.Unlock()
		return
	}
	rel := updateOffer
	updateBusy = true
	updateMu.Unlock()
	procEnableWindow.Call(updateButton, 0)
	state.progress(1, "скачиваю обновление "+rel.Version)
	go func() {
		err := worker.InstallRelease(ctx, rel, func(done, total int64) {
			if total > 0 {
				state.progress(int(done*100/total), "скачиваю обновление "+rel.Version)
			}
		})
		if err != nil {
			updateMu.Lock()
			updateBusy = false
			updateMu.Unlock()
			state.progress(0, "обновление не установилось")
			state.log(err.Error())
			procEnableWindow.Call(updateButton, 1)
			return
		}
		state.log("обновление скачано, программа перезапускается")
		procPostMessage.Call(hwnd, wmAppUpdate, 0, 0)
	}()
}

// refreshUI переносит состояние рабочего потока в элементы окна.
//
// Вызывается по таймеру четыре раза в секунду, поэтому здесь нельзя делать
// ничего дорогого: перезапись журнала только при изменении, обращение к файлу
// конфигурации — не чаще раза в две секунды, обновление трея — тоже.
func refreshUI() {
	percent, status, logText, busy, finished, failed, _, _ := state.snapshot()

	procSetWindowText.Call(stateStatus, uintptr(unsafe.Pointer(utf16Ptr(status))))
	procSendMessage.Call(stateBar, pbmSetPos, uintptr(percent), 0)
	if weightStatus != 0 && time.Since(lastWeightScan) >= 2*time.Second {
		lastWeightScan = time.Now()
		text := formatWeight(worker.DownloadedWeightBytes())
		if text != lastWeightText {
			lastWeightText = text
			procSetWindowText.Call(weightStatus, uintptr(unsafe.Pointer(utf16Ptr(text))))
		}
	}
	applyDraftProgress()

	updateMu.Lock()
	known, version := updateKnown, updateOffer.Version
	updateMu.Unlock()
	if known && updateButton != 0 && version != shownUpdate {
		shownUpdate = version
		procShowWindow.Call(updateButton, swShow)
		state.log("доступна новая версия " + version)
	}

	if logBox != 0 && len(logText) != lastLogLength {
		lastLogLength = len(logText)
		procSetWindowText.Call(logBox, uintptr(unsafe.Pointer(utf16Ptr(logText))))
	}

	apiType, baseURL, model, apiKey := state.harnessFields()
	if apiType != lastHarnessType {
		lastHarnessType = apiType
		setFieldText(fieldType, apiType)
		procEnableWindow.Call(getControl(mainHwnd, idCopyType), boolFlag(apiType != ""))
	}
	if baseURL != lastHarnessURL {
		lastHarnessURL = baseURL
		setFieldText(fieldURL, baseURL)
		procEnableWindow.Call(getControl(mainHwnd, idCopyURL), boolFlag(baseURL != ""))
	}
	if apiKey != lastHarnessKey {
		lastHarnessKey = apiKey
		setFieldText(fieldKey, apiKey)
		procEnableWindow.Call(getControl(mainHwnd, idCopyKey), boolFlag(apiKey != ""))
	}
	if model != lastHarnessModel {
		lastHarnessModel = model
		setFieldText(fieldModel, model)
		procEnableWindow.Call(getControl(mainHwnd, idCopyModel), boolFlag(model != ""))
	}

	now := time.Now()
	if now.Sub(lastTrayUpdate) < 2*time.Second {
		return
	}
	lastTrayUpdate = now

	var tip string
	switch {
	case busy:
		tip = fmt.Sprintf("Compute Network: %d%% — %s", percent, status)
	case failed:
		tip = "Compute Network: ошибка подключения"
	case agentRunning() && nodePaused():
		tip = "Compute Network: узел на паузе"
	case agentRunning():
		tip = "Compute Network: узел работает"
	case finished:
		tip = "Compute Network: узел подключён, агент остановлен"
	default:
		tip = "Compute Network: ждём приглашение"
	}
	updateTrayTip(mainHwnd, tip)
}

var (
	lastLogLength    int
	lastTrayUpdate   time.Time
	lastWeightScan   time.Time
	lastWeightText   string
	lastHarnessType  string
	lastHarnessURL   string
	lastHarnessKey   string
	lastHarnessModel string
)

var (
	draftAnnounced bool
	draftFinished  bool
	lastDraftScan  time.Time
)

// applyDraftProgress двигает полоску по байтам, которые уже лежат на диске.
// Процент в статусе сам по себе не двигался: он оставался на одном числе,
// пока файл головы не докачается целиком.
func applyDraftProgress() {
	if time.Since(lastDraftScan) < 400*time.Millisecond {
		return
	}
	lastDraftScan = time.Now()
	updateMu.Lock()
	updating := updateBusy
	updateMu.Unlock()
	if updating {
		return
	}
	done, total, active := worker.DraftHeadProgress()
	if active {
		draftFinished = false
		if !draftAnnounced {
			draftAnnounced = true
			state.log("скачиваю голову черновика")
		}
		pct := 0
		if total > 0 {
			pct = int(done * 100 / total)
			if pct > 99 {
				pct = 99
			}
		}
		text := fmt.Sprintf("голова черновика: %d%% · %s из %s", pct, formatWeight(done), formatWeight(total))
		procSetWindowText.Call(stateStatus, uintptr(unsafe.Pointer(utf16Ptr(text))))
		procSendMessage.Call(stateBar, pbmSetPos, uintptr(pct), 0)
		return
	}
	if draftAnnounced && !draftFinished && total > 0 && done >= total-4096 {
		draftFinished = true
		state.progress(100, "голова черновика скачана")
		state.log("голова черновика скачана")
		procSetWindowText.Call(stateStatus, uintptr(unsafe.Pointer(utf16Ptr("голова черновика скачана"))))
		procSendMessage.Call(stateBar, pbmSetPos, 100, 0)
	}
}

func formatWeight(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1f ГБ", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.0f МБ", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.0f КБ", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d Б", n)
	}
}

func openFolder(hwnd uintptr) {
	state.mu.Lock()
	config := state.config
	state.mu.Unlock()
	dir := filepath.Dir(config)
	if config == "" {
		dir = filepath.Dir(defaultConfigPath())
	}
	openExplorer(dir)
}

// runGUI запускает окно установщика и трей. Возвращает код выхода.
func runGUI() int {
	// Окно Win32 принадлежит потоку, который его создал: очередь сообщений и
	// таймеры читает только он. Без привязки планировщик Go может увести
	// горутину на другой поток — тогда окно перестаёт отвечать: значок в трее
	// не открывает окно, подсказка не обновляется, а IsHungAppWindow сообщает
	// о зависании. Поток держим до конца жизни окна.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	parseArgs()
	// Визуальные стили и шрифт включаем до создания окна: без манифеста
	// comctl32 окно рисуется в классическом стиле Windows 2000 — с «вдавленными»
	// кнопками и мелким шрифтом.
	activateVisualStyles()
	uiFont = createUIFont()
	headingFont = createHeadingFont()

	// У приложения не должно быть консольного окна.
	if console, _, _ := procGetConsoleWindow.Call(); console != 0 {
		hideConsole(console)
	}

	// Второй запуск не создаёт второе приложение: он показывает окно первого.
	// Это и ожидаемое поведение, и запасной путь, если значок в трее не отвечает.
	if hwnd := existingWindow(); hwnd != 0 {
		procPostMessage.Call(hwnd, wmAppShow, 0, 0)
		return 0
	}

	return run()
}

// wmAppShow — просьба показать окно от второго экземпляра программы.
const wmAppShow = 0x8000 + 2

// showMainWindow показывает окно и выводит его на передний план.
func showMainWindow(hwnd uintptr) {
	const (
		swpNoMove      = 0x0002
		swpNoSize      = 0x0001
		swpShowWindow  = 0x0040
		hwndTopMost    = ^uintptr(0) // (HWND)-1
		hwndNotTopMost = ^uintptr(1) // (HWND)-2
	)
	// Скрытый установщик обновления передаёт «не показывать». Первый
	// ShowWindow Windows подменяет этой командой, поэтому показ повторяется.
	procShowWindow.Call(hwnd, swShow)
	procShowWindow.Call(hwnd, swShow)
	procShowWindow.Call(hwnd, 9) // SW_RESTORE
	procSetWindowPos.Call(hwnd, hwndTopMost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)

	// Очередь ввода переднего окна на короткое время становится нашей:
	// иначе SetForegroundWindow молча отказывает процессу, который запустил
	// фоновый установщик.
	fg, _, _ := procGetForeground.Call()
	current, _, _ := procGetCurrentThreadId.Call()
	var fgThread uintptr
	if fg != 0 && fg != hwnd {
		fgThread, _, _ = procGetWindowThread.Call(fg, 0)
	}
	attached := false
	if fgThread != 0 && fgThread != current {
		ok, _, _ := procAttachThreadInput.Call(current, fgThread, 1)
		attached = ok != 0
	}
	procBringWindowToTop.Call(hwnd)
	procSetForeground.Call(hwnd)
	if attached {
		procAttachThreadInput.Call(current, fgThread, 0)
	}
	procSetWindowPos.Call(hwnd, hwndNotTopMost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpShowWindow)
	flashUntilForeground(hwnd)
}

// revealAfterUpdate несколько секунд добивается, чтобы окно стало видимым.
// Ранние ShowWindow скрытый запуск обновления ещё успевает отменить.
func revealAfterUpdate(hwnd uintptr) {
	if argRun || hwnd == 0 || revealDone {
		return
	}
	revealTries++
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	fg, _, _ := procGetForeground.Call()
	if visible != 0 && fg == hwnd {
		revealDone = true
		return
	}
	if visible == 0 {
		procShowWindow.Call(hwnd, swShow)
		procShowWindow.Call(hwnd, 9) // SW_RESTORE
	}
	if revealTries == 4 || revealTries == 8 {
		showMainWindow(hwnd)
	}
	if revealTries >= 16 {
		revealDone = true
	}
}

type flashInfo struct {
	cbSize  uint32
	hwnd    uintptr
	flags   uint32
	count   uint32
	timeout uint32
}

// flashUntilForeground мигает кнопкой на панели задач, пока окно не станет
// передним. Скрытый установщик обновления часто не имеет права сам вывести
// окно вперёд, и без мигания кажется, что программа не открылась.
func flashUntilForeground(hwnd uintptr) {
	info := flashInfo{
		hwnd:    hwnd,
		flags:   0x00000003 | 0x0000000C, // FLASHW_ALL | FLASHW_TIMERNOFG
		count:   0,
		timeout: 0,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&info)))
}

// existingWindow ищет уже открытое окно этой программы.
//
// Нужен, чтобы второй запуск не создавал второе приложение с вторым значком в
// трее, а просто показывал окно первого — это ожидаемое поведение и запасной
// путь на случай, когда значок в трее «призрачный» и на клик не реагирует.
func existingWindow() uintptr {
	hwnd, _, _ := procFindWindow.Call(
		uintptr(unsafe.Pointer(utf16Ptr(windowClassName))),
		0,
	)
	return hwnd
}
