//go:build windows

// AFK-рыбалка для Roblox: один exe без установки.
//
// Как это работает (простое компьютерное зрение — сравнение цвета пикселей):
//  1. Заброс: держит ЛКМ, пока заполняется шкала «ЗАРЯД», и отпускает.
//  2. Поклёвка: снимает квадрат 13×13 у маркера F6 и сравнивает цвет.
//  3. Пока маркер виден — кликает в отдельной игровой точке F8.
//  4. Исчезновение маркера завершает цикл, но не доказывает успешный улов.
//     После паузы пробует следующий заброс; при неопределённом состоянии стоп.
//
// Клавиши: F6 — запомнить полосу, F7 — идеальный заброс (необязательно),
// F5 — старт/стоп, F10 — выход.
package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// ---------------------------------------------------------------- настройки

type configFile struct {
	CastHoldMs    int    `json:"cast_hold_ms"`
	CastMaxHoldMs int    `json:"cast_max_hold_ms"`
	BiteTimeoutS  int    `json:"bite_timeout_s"`
	ClickEveryMs  int    `json:"click_every_ms"`
	ClickJitterPx int    `json:"click_jitter_px"`
	ClickX        int    `json:"click_x"`
	ClickY        int    `json:"click_y"`
	ClickReady    bool   `json:"click_ready"`
	CatchMaxS     int    `json:"catch_max_s"`
	GoneMs        int    `json:"gone_ms"`
	CooldownMinMs int    `json:"cooldown_min_ms"`
	CooldownMaxMs int    `json:"cooldown_max_ms"`
	Tolerance     int    `json:"tolerance"`
	BarX          int    `json:"bar_x"`
	BarY          int    `json:"bar_y"`
	BarColor      string `json:"bar_color"`
	PerfX         int    `json:"perf_x"`
	PerfY         int    `json:"perf_y"`
	PerfColor     string `json:"perf_color"`
}

var defaults = configFile{CastHoldMs: 1050, CastMaxHoldMs: 2500, BiteTimeoutS: 90, ClickEveryMs: 55,
	ClickJitterPx: 12, CatchMaxS: 25, GoneMs: 350, CooldownMinMs: 4000, CooldownMaxMs: 4000, Tolerance: 35}

var cfgPath string

func loadConfig() configFile {
	c := defaults
	if b, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	c.CastHoldMs = clamp(c.CastHoldMs, 100, 5000)
	c.CastMaxHoldMs = clamp(c.CastMaxHoldMs, 300, 6000)
	c.ClickEveryMs = clamp(c.ClickEveryMs, 30, 1000)
	c.ClickJitterPx = clamp(c.ClickJitterPx, 0, 25)
	c.BiteTimeoutS = clamp(c.BiteTimeoutS, 5, 300)
	c.CatchMaxS = clamp(c.CatchMaxS, 5, 120)
	c.GoneMs = clamp(c.GoneMs, 250, 3000)
	// Между циклами случайная пауза 4,2–4,8 секунды.
	c.CooldownMinMs = 4200
	c.CooldownMaxMs = 4800
	c.Tolerance = clamp(c.Tolerance, 0, 60)
	return c
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func saveConfig(c configFile) {
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(cfgPath, b, 0o644)
}

// ---------------------------------------------------------------- Win32

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pRegisterHotKey          = user32.NewProc("RegisterHotKey")
	pGetMessageW             = user32.NewProc("GetMessageW")
	pGetCursorPos            = user32.NewProc("GetCursorPos")
	pSetCursorPos            = user32.NewProc("SetCursorPos")
	pSendInput               = user32.NewProc("SendInput")
	pGetDC                   = user32.NewProc("GetDC")
	pReleaseDC               = user32.NewProc("ReleaseDC")
	pGetForegroundWindow     = user32.NewProc("GetForegroundWindow")
	pGetWindowRect           = user32.NewProc("GetWindowRect")
	pGetWindowThreadProcId   = user32.NewProc("GetWindowThreadProcessId")
	pSetProcessDpiAwareCtx   = user32.NewProc("SetProcessDpiAwarenessContext")
	pSetProcessDPIAware      = user32.NewProc("SetProcessDPIAware")
	pCreateCompatibleDC      = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap  = gdi32.NewProc("CreateCompatibleBitmap")
	pSelectObject            = gdi32.NewProc("SelectObject")
	pBitBlt                  = gdi32.NewProc("BitBlt")
	pGetDIBits               = gdi32.NewProc("GetDIBits")
	pDeleteObject            = gdi32.NewProc("DeleteObject")
	pDeleteDC                = gdi32.NewProc("DeleteDC")
	pOpenProcess             = kernel32.NewProc("OpenProcess")
	pQueryFullProcessImageNm = kernel32.NewProc("QueryFullProcessImageNameW")
	pCloseHandle             = kernel32.NewProc("CloseHandle")
	pSetConsoleTitleW        = kernel32.NewProc("SetConsoleTitleW")
	pSetConsoleOutputCP      = kernel32.NewProc("SetConsoleOutputCP")
)

const (
	vkF5, vkF6, vkF7, vkF8, vkF10 = 0x74, 0x75, 0x76, 0x77, 0x79
	wmHotkey                      = 0x0312
	mouseLeftDown                 = 0x0002
	mouseLeftUp                   = 0x0004
	srcCopy                       = 0x00CC0020
	captureBlt                    = 0x40000000
)

type point struct{ X, Y int32 }

type rect struct{ Left, Top, Right, Bottom int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	_       uint32
}

// INPUT с MOUSEINPUT: 40 байт на amd64.
type mouseInput struct {
	Type      uint32
	_         uint32
	Dx, Dy    int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

type bitmapInfoHeader struct {
	Size          uint32
	Width, Height int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

func mouse(flags uint32) bool {
	in := mouseInput{Type: 0, Flags: flags}
	n, _, err := pSendInput.Call(1, uintptr(unsafe.Pointer(&in)), unsafe.Sizeof(in))
	if n != 1 {
		running.Store(false)
		fmt.Printf("Ошибка SendInput: отправлено %d из 1, %v. Автоматизация остановлена.\n", n, err)
		return false
	}
	return true
}

func down() bool { return mouse(mouseLeftDown) }
func up() bool   { return mouse(mouseLeftUp) }
func tap() {
	down()
	time.Sleep(jitter(15, 4))
	up()
}

func cursor() (int, int) {
	var p point
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return int(p.X), int(p.Y)
}

func moveCursor(x, y int) {
	pSetCursorPos.Call(uintptr(x), uintptr(y))
}

func robloxRect() (rect, bool) {
	hwnd, _, _ := pGetForegroundWindow.Call()
	if hwnd == 0 {
		return rect{}, false
	}
	var r rect
	if ok, _, _ := pGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return rect{}, false
	}
	return r, true
}

func clickPoint(c configFile) (int, int, bool) {
	if !running.Load() || !robloxActive() {
		return 0, 0, false
	}
	r, ok := robloxRect()
	if !ok || !c.ClickReady {
		return 0, 0, false
	}
	spread := c.ClickJitterPx
	x := c.ClickX + rand.Intn(2*spread+1) - spread
	y := c.ClickY + rand.Intn(2*spread+1) - spread
	// Never silently substitute another position after the window moves.
	if x <= int(r.Left)+8 || x >= int(r.Right)-8 || y <= int(r.Top)+32 || y >= int(r.Bottom)-8 {
		return 0, 0, false
	}
	if abs(x-c.BarX) <= 45 && abs(y-c.BarY) <= 45 {
		return 0, 0, false
	}
	return x, y, true
}

func prepareClick(c configFile) bool {
	x, y, ok := clickPoint(c)
	if !ok {
		return false
	}
	if r, _, _ := pSetCursorPos.Call(uintptr(x), uintptr(y)); r == 0 {
		return false
	}
	return running.Load() && robloxActive()
}

func gameClick(c configFile) bool {
	if !prepareClick(c) {
		return false
	}
	tap()
	return running.Load()
}

// grab снимает прямоугольник экрана в BGRA (сверху вниз).
func grab(x, y, w, h int) []byte {
	screen, _, _ := pGetDC.Call(0)
	defer pReleaseDC.Call(0, screen)
	mem, _, _ := pCreateCompatibleDC.Call(screen)
	defer pDeleteDC.Call(mem)
	bmp, _, _ := pCreateCompatibleBitmap.Call(screen, uintptr(w), uintptr(h))
	defer pDeleteObject.Call(bmp)
	old, _, _ := pSelectObject.Call(mem, bmp)
	pBitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), screen, uintptr(x), uintptr(y), srcCopy|captureBlt)
	pSelectObject.Call(mem, old)
	bi := bitmapInfoHeader{Width: int32(w), Height: -int32(h), Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	buf := make([]byte, w*h*4)
	pGetDIBits.Call(mem, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), 0)
	return buf
}

func pixel(x, y int) string {
	b := grab(x, y, 1, 1)
	return fmt.Sprintf("#%02X%02X%02X", b[2], b[1], b[0])
}

func parseColor(s string) (r, g, b int, ok bool) {
	if len(s) != 7 || s[0] != '#' {
		return
	}
	var v int
	if _, err := fmt.Sscanf(s[1:], "%06X", &v); err != nil {
		return
	}
	return v >> 16 & 0xFF, v >> 8 & 0xFF, v & 0xFF, true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// seen: есть ли цвет color (с допуском tol на канал) в квадрате ±r вокруг точки.
func seen(x, y int, color string, r, tol int) bool {
	cr, cg, cb, ok := parseColor(color)
	if !ok {
		return false
	}
	w := 2*r + 1
	b := grab(x-r, y-r, w, w)
	for i := 0; i+3 < len(b); i += 4 {
		if abs(int(b[i+2])-cr) <= tol && abs(int(b[i+1])-cg) <= tol && abs(int(b[i])-cb) <= tol {
			return true
		}
	}
	return false
}

// robloxActive: активно ли окно Roblox (обычный клиент или версия из Microsoft Store).
func robloxActive() bool {
	hwnd, _, _ := pGetForegroundWindow.Call()
	if hwnd == 0 {
		return false
	}
	var pid uint32
	pGetWindowThreadProcId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	h, _, _ := pOpenProcess.Call(0x1000, 0, uintptr(pid)) // PROCESS_QUERY_LIMITED_INFORMATION
	if h == 0 {
		return false
	}
	defer pCloseHandle.Call(h)
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	if r, _, _ := pQueryFullProcessImageNm.Call(h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return false
	}
	name := strings.ToLower(filepath.Base(syscall.UTF16ToString(buf[:n])))
	return name == "robloxplayerbeta.exe" || name == "windows10universal.exe"
}

func title(s string) {
	p, _ := syscall.UTF16PtrFromString(s)
	pSetConsoleTitleW.Call(uintptr(unsafe.Pointer(p)))
}

// ---------------------------------------------------------------- логика

var (
	running    atomic.Bool
	workerBusy atomic.Bool
	catches    atomic.Int64
	cfg        configFile
)

func say(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), s)
	state := "стоп"
	if running.Load() {
		state = "работает"
	}
	title(fmt.Sprintf("AFK-рыбалка — %s — поймано: %d", state, catches.Load()))
}

// sleep спит, просыпаясь при остановке; false — остановили.
func sleep(d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if !running.Load() {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return running.Load()
}

// waitRoblox ждёт активного окна Roblox; false — остановили.
func waitRoblox() bool {
	warned := false
	for running.Load() && !robloxActive() {
		up()
		if !warned {
			say("Окно Roblox не активно — жду")
			warned = true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return running.Load()
}

func jitter(ms, spread int) time.Duration {
	return time.Duration(ms+rand.Intn(2*spread+1)-spread) * time.Millisecond
}

func barVisible(c configFile) bool { return seen(c.BarX, c.BarY, c.BarColor, 6, c.Tolerance) }

// The worker owns an immutable configuration snapshot. Calibration is only
// allowed after it has exited, and a second worker cannot start meanwhile.
func fishLoop(c configFile) {
	defer func() { up(); running.Store(false); workerBusy.Store(false) }()
	stopError := func() {
		say("Клик не отправлен: проверьте F8, положение окна и фокус Roblox. Стоп")
	}
	for running.Load() {
		if !waitRoblox() {
			return
		}
		if barVisible(c) {
			say("Маркер F6 виден до заброса. Завершите текущую рыбалку или перекалибруйте F6. Стоп")
			return
		}
		if !prepareClick(c) {
			stopError()
			return
		}
		say("Попытка заброса: удерживаю ЛКМ в игровой точке F8")
		if !down() {
			return
		}
		start := time.Now()
		chargeSeen := false
		hold := jitter(c.CastHoldMs, 45)
		limit := hold
		if c.PerfColor != "" {
			limit = time.Duration(c.CastMaxHoldMs) * time.Millisecond
		}
		for running.Load() && time.Since(start) < limit {
			if !robloxActive() {
				up()
				say("Потерян фокус во время заброса. Стоп")
				return
			}
			if c.PerfColor != "" && time.Since(start) >= 250*time.Millisecond && seen(c.PerfX, c.PerfY, c.PerfColor, 3, c.Tolerance) {
				chargeSeen = true
				break
			}
			if !sleep(jitter(18, 4)) {
				break
			}
		}
		up()
		if !running.Load() {
			return
		}
		if c.PerfColor != "" && !chargeSeen {
			say("Полная шкала F7 не обнаружена: заброс НЕ подтверждён. Проверьте F7/F8. Стоп")
			return
		}
		say("Нажатие отправлено; жду маркер поклёвки (это ещё не подтверждение заброса)")
		start = time.Now()
		var visibleSince time.Time
		bitten := false
		for running.Load() && time.Since(start) < time.Duration(c.BiteTimeoutS)*time.Second {
			if !robloxActive() {
				say("Потерян фокус: цикл остановлен, F5 — новый запуск")
				return
			}
			if barVisible(c) {
				if visibleSince.IsZero() {
					visibleSince = time.Now()
				}
				if time.Since(visibleSince) >= 120*time.Millisecond {
					bitten = true
					break
				}
			} else {
				visibleSince = time.Time{}
			}
			if !sleep(jitter(35, 7)) {
				return
			}
		}
		if !running.Load() {
			return
		}
		if !bitten {
			say("За %d с поклёвка не обнаружена. Не могу отличить незаброс от пропущенного маркера. Стоп; проверьте F6/F8", c.BiteTimeoutS)
			return
		}
		say("Маркер поклёвки виден. Кликаю в точке F8, НЕ по кнопке Остановка")
		start = time.Now()
		var gone time.Time
		finished := false
		for running.Load() && time.Since(start) < time.Duration(c.CatchMaxS)*time.Second {
			if !robloxActive() {
				say("Потерян фокус во время вываживания. Стоп")
				return
			}
			if barVisible(c) {
				gone = time.Time{}
				if !gameClick(c) {
					stopError()
					return
				}
				if time.Since(start) < 250*time.Millisecond {
					say("Первый клик отправлен в точку F8 (%d,%d)", c.ClickX, c.ClickY)
				}
			} else {
				if gone.IsZero() {
					gone = time.Now()
				}
				if time.Since(gone) >= time.Duration(c.GoneMs)*time.Millisecond {
					finished = true
					break
				}
			}
			if !sleep(jitter(c.ClickEveryMs, 12)) {
				return
			}
		}
		if !running.Load() {
			return
		}
		if !finished {
			say("Тайм-аут вываживания, улов не подтверждён. Стоп")
			return
		}
		catches.Add(1)
		say("Полоса исчезла. Завершено циклов: %d (успешный улов отдельно не распознаётся)", catches.Load())
		pause := c.CooldownMinMs + rand.Intn(c.CooldownMaxMs-c.CooldownMinMs+1)
		say("Пауза %d мс перед следующим забросом", pause)
		if !sleep(time.Duration(pause) * time.Millisecond) {
			return
		}
	}
}

func main() {
	if r, _, _ := pSetProcessDpiAwareCtx.Call(^uintptr(3)); r == 0 { // PER_MONITOR_AWARE_V2
		pSetProcessDPIAware.Call()
	}
	pSetConsoleOutputCP.Call(65001)
	exe, _ := os.Executable()
	cfgPath = filepath.Join(filepath.Dir(exe), "afk_fishing.json")
	cfg = loadConfig()
	saveConfig(cfg)

	fmt.Println("AFK-рыбалка для Roblox — версия 2.2, пошаговые подсказки")
	fmt.Println("  F6 — только запомнить цвет красной заливки Остановка во время поклёвки, НЕ нажимать ЛКМ")
	fmt.Println("  F8 — запомнить свободную игровую точку над водой для заброса и кликов, вдали от кнопок")
	fmt.Println("  F7 — необязательно: цвет верха полной шкалы ЗАРЯД; повторное нажатие очищает F7")
	fmt.Println("  Перед F5 завершите ручной улов. После перемещения/масштабирования окна повторите калибровку.")
	fmt.Println("  F5  — старт / стоп,  F10 — выход")
	fmt.Println("  Настройки:", cfgPath)
	fmt.Println()
	if cfg.BarColor == "" {
		say("Сначала откалибруйте полосу клавишей F6")
	} else {
		say("Полоса откалибрована (%s в %d,%d). F5 — старт", cfg.BarColor, cfg.BarX, cfg.BarY)
	}

	runtime.LockOSThread()
	initHints()
	defer closeHints()
	for id, vk := range map[int]uintptr{5: vkF5, 6: vkF6, 7: vkF7, 8: vkF8, 10: vkF10} {
		if r, _, _ := pRegisterHotKey.Call(0, uintptr(id), 0, vk); r == 0 {
			say("Не удалось занять клавишу F%d — её держит другая программа", id)
		}
	}
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		updateHint()
		if m.Message != wmHotkey {
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
			continue
		}
		if m.WParam == 6 || m.WParam == 7 || m.WParam == 8 {
			if workerBusy.Load() {
				say("Сначала остановите F5 и дождитесь завершения цикла")
				continue
			}
			if !robloxActive() {
				say("Для калибровки сделайте окно Roblox активным")
				continue
			}
		}
		switch m.WParam {
		case 8:
			if hintStep == 1 {
				say("Сначала шаг 1: F6 во время поклёвки")
				continue
			}
			cfg.ClickX, cfg.ClickY = cursor()
			cfg.ClickReady = true
			saveConfig(cfg)
			say("Игровая точка F8: %d,%d. Здесь не должно быть кнопок интерфейса", cfg.ClickX, cfg.ClickY)
			hintStep = 3
			updateHint()
		case 6:
			x, y := cursor()
			r, ok := robloxRect()
			if !ok {
				say("Не удалось определить окно для F6")
				continue
			}
			// Remove hover highlighting before sampling the detector pixel.
			parkX := int(r.Left + (r.Right-r.Left)/2)
			parkY := int(r.Top + (r.Bottom-r.Top)/3)
			if abs(parkX-x) < 60 && abs(parkY-y) < 60 {
				parkY = int(r.Top + 2*(r.Bottom-r.Top)/3)
			}
			moveCursor(parkX, parkY)
			time.Sleep(450 * time.Millisecond)
			if !robloxActive() {
				say("Фокус изменился: F6 не сохранена")
				continue
			}
			cfg.BarX, cfg.BarY = x, y
			cfg.BarColor = pixel(x, y)
			saveConfig(cfg)
			say("F6 без наведения: %s в %d,%d. Проверьте, что кнопка оставалась видна во время измерения", cfg.BarColor, x, y)
			if hintStep == 1 {
				hintStep = 2
				updateHint()
			}
		case 7:
			if cfg.PerfColor != "" {
				cfg.PerfColor = ""
				saveConfig(cfg)
				say("F7 очищена: заброс по времени")
				continue
			}
			cfg.PerfX, cfg.PerfY = cursor()
			cfg.PerfColor = pixel(cfg.PerfX, cfg.PerfY)
			saveConfig(cfg)
			say("Идеальный заброс: %s в %d,%d", cfg.PerfColor, cfg.PerfX, cfg.PerfY)
		case 5:
			if running.Load() {
				running.Store(false)
				up()
				say("Стоп")
			} else {
				if workerBusy.Load() {
					say("Предыдущий цикл ещё останавливается")
					continue
				}
				cfg = loadConfig()
				if cfg.BarColor == "" || !cfg.ClickReady {
					say("Сначала настройте F6 (маркер) и F8 (место клика)")
					continue
				}
				workerBusy.Store(true)
				running.Store(true)
				say("Старт")
				go fishLoop(cfg)
			}
		case 10:
			running.Store(false)
			up()
			return
		}
	}
}
