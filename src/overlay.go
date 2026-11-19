//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// A standard Win32 STATIC popup: no browser, injected code or dependencies.
// All calls happen on the main locked OS thread.
var hintWindow uintptr
var hintStep = 1
var hintText string
var hintShown bool

var (
	pCreateWindowExW            = user32.NewProc("CreateWindowExW")
	pSetWindowTextW             = user32.NewProc("SetWindowTextW")
	pInvalidateRect             = user32.NewProc("InvalidateRect")
	pUpdateWindow               = user32.NewProc("UpdateWindow")
	pShowWindow                 = user32.NewProc("ShowWindow")
	pDestroyWindow              = user32.NewProc("DestroyWindow")
	pSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	pGetSystemMetrics           = user32.NewProc("GetSystemMetrics")
	pSetTimer                   = user32.NewProc("SetTimer")
	pKillTimer                  = user32.NewProc("KillTimer")
	pDispatchMessageW           = user32.NewProc("DispatchMessageW")
	pTranslateMessage           = user32.NewProc("TranslateMessage")
	pSendMessageW               = user32.NewProc("SendMessageW")
	pGetStockObject             = gdi32.NewProc("GetStockObject")
)

func initHints() {
	class, _ := syscall.UTF16PtrFromString("STATIC")
	text, _ := syscall.UTF16PtrFromString("")
	screenW, _, _ := pGetSystemMetrics.Call(0)
	width := 620
	if int(screenW) < width+32 {
		width = int(screenW) - 32
	}
	if width < 200 {
		return
	}
	// TOPMOST | TRANSPARENT | TOOLWINDOW | LAYERED | NOACTIVATE.
	const exStyle = 0x8 | 0x20 | 0x80 | 0x80000 | 0x08000000
	// POPUP | BORDER | SS_CENTER | SS_CENTERIMAGE (multiline text uses default layout).
	hintWindow, _, _ = pCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(text)),
		0x80000000|0x00800000|1, (screenW-uintptr(width))/2, 24, uintptr(width), 112, 0, 0, 0, 0)
	if hintWindow == 0 {
		say("Не удалось показать подсказки. Клавиши доступны в консоли")
		return
	}
	pSetLayeredWindowAttributes.Call(hintWindow, 0, 245, 2)
	font, _, _ := pGetStockObject.Call(17)          // DEFAULT_GUI_FONT, borrowed handle.
	pSendMessageW.Call(hintWindow, 0x0030, font, 1) // WM_SETFONT
	pSetTimer.Call(0, 900, 150, 0)
	updateHint()
}

func updateHint() {
	if hintWindow == 0 {
		return
	}
	// Both calibration steps are complete: do not keep an extra window over Roblox.
	if cfg.BarColor != "" && cfg.ClickReady {
		if hintShown {
			pShowWindow.Call(hintWindow, 0)
			hintShown = false
		}
		return
	}
	if running.Load() || workerBusy.Load() {
		if hintShown {
			pShowWindow.Call(hintWindow, 0)
			hintShown = false
		}
		return
	}
	text := ""
	switch hintStep {
	case 1:
		text = "Шаг 1 из 3\r\nДождись поклёвки. Наведи на красную кнопку «Остановка».\r\nНажми F6. Мышью кнопку не нажимай.\r\nF10 — закрыть программу"
	case 2:
		text = "Шаг 2 из 3\r\nЗаверши ручную рыбалку. Наведи курсор на воду, вдали от кнопок.\r\nНажми F8 — здесь программа будет кликать.\r\nF10 — закрыть программу"
	default:
		text = "Всё готово\r\nОткрой игру и нажми F5, чтобы начать.\r\nF5 — остановить. F10 — выйти.\r\nЕсли поклёвка не определяется — настрой F6 заново."
	}
	if text != hintText {
		p, _ := syscall.UTF16PtrFromString(text)
		pSetWindowTextW.Call(hintWindow, uintptr(unsafe.Pointer(p)))
		pInvalidateRect.Call(hintWindow, 0, 1)
		pUpdateWindow.Call(hintWindow)
		hintText = text
	}
	if !hintShown {
		pShowWindow.Call(hintWindow, 4)
		hintShown = true
	} // SW_SHOWNOACTIVATE
}

func closeHints() {
	pKillTimer.Call(0, 900)
	if hintWindow != 0 {
		pDestroyWindow.Call(hintWindow)
		hintWindow = 0
	}
}
