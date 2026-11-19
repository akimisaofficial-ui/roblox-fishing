#Requires AutoHotkey v2.0
#SingleInstance Force
; ============================================================================
;  AFK-рыбалка для Roblox (AutoHotkey v2)
;
;  Как устроена рыбалка в игре:
;    1. Заброс: удерживать ЛКМ, шкала «ЗАРЯД» заполняется ~1 с; отпустить на
;       полной шкале -> «PERFECT!» (бонус к удаче).
;    2. Поклёвка: над поплавком «!», и внизу экрана появляется полоса
;       «Нажимайте быстро! (000)».
;    3. Быстро кликать, пока полоса не заполнится -> всплывает улов.
;
;  Скрипт следит за полосой «Нажимайте быстро!»: она всегда в одном месте
;  экрана, в отличие от «!» над поплавком.
;
;  Клавиши:
;    F6  — запомнить полосу «Нажимайте быстро!». Когда полоса на экране,
;          наведите курсор на КРАСНУЮ кнопку у её правого края и нажмите F6.
;    F7  — (необязательно) идеальный заброс: зажмите ЛКМ, дождитесь полной
;          шкалы «ЗАРЯД», наведите курсор на ВЕРХ зелёной заливки и нажмите F7.
;          Скрипт будет отпускать ЛКМ, когда шкала дойдёт до этой точки.
;          Без F7 заброс держится CastHoldMs миллисекунд.
;    F5  — старт / стоп
;    F10 — выход
;
;  Настройки — в afk_fishing.ini рядом со скриптом (появится после F6).
;  Кликает только в активном окне Roblox.
; ============================================================================

CoordMode "Pixel", "Screen"
CoordMode "Mouse", "Screen"
SendMode "Event"
SetMouseDelay 10

global Ini := A_ScriptDir "\afk_fishing.ini"
global Running := false
global Catches := 0
global RobloxWin := "ahk_exe RobloxPlayerBeta.exe"

; ---- настройки ------------------------------------------------------------
global CastHoldMs     := Cfg("fishing", "CastHoldMs", 1050)     ; держать ЛКМ при забросе (без F7)
global CastMaxHoldMs  := Cfg("fishing", "CastMaxHoldMs", 2500)  ; предел удержания при F7
global BiteTimeoutS   := Cfg("fishing", "BiteTimeoutS", 90)     ; нет поклёвки — перезаброс
global ClickEveryMs   := Cfg("fishing", "ClickEveryMs", 55)     ; частота кликов на полосе
global CatchMaxS      := Cfg("fishing", "CatchMaxS", 25)        ; предел на одну рыбу
global GoneMs         := Cfg("fishing", "GoneMs", 350)          ; полоса пропала на столько — поймано
global CooldownMinMs  := Cfg("fishing", "CooldownMinMs", 2000)  ; пауза после улова
global CooldownMaxMs  := Cfg("fishing", "CooldownMaxMs", 3000)
global Tolerance      := Cfg("detect", "Tolerance", 35)         ; допуск по цвету 0-255
global BarX := Cfg("detect", "BarX", 0), BarY := Cfg("detect", "BarY", 0)
global BarColor := IniRead(Ini, "detect", "BarColor", "")
global PerfX := Cfg("detect", "PerfX", 0), PerfY := Cfg("detect", "PerfY", 0)
global PerfColor := IniRead(Ini, "detect", "PerfColor", "")

Status(BarColor = "" ? "F6 — запомнить полосу «Нажимайте быстро!»" : "F5 — старт")

; ---- калибровка -----------------------------------------------------------
F6:: {
    global BarX, BarY, BarColor
    MouseGetPos &BarX, &BarY
    BarColor := PixelGetColor(BarX, BarY)
    IniWrite BarX, Ini, "detect", "BarX"
    IniWrite BarY, Ini, "detect", "BarY"
    IniWrite BarColor, Ini, "detect", "BarColor"
    IniWrite Tolerance, Ini, "detect", "Tolerance"
    Status("Полоса запомнена: " BarColor " в " BarX "," BarY ". F5 — старт")
}
F7:: {
    global PerfX, PerfY, PerfColor
    MouseGetPos &PerfX, &PerfY
    PerfColor := PixelGetColor(PerfX, PerfY)
    IniWrite PerfX, Ini, "detect", "PerfX"
    IniWrite PerfY, Ini, "detect", "PerfY"
    IniWrite PerfColor, Ini, "detect", "PerfColor"
    Status("Идеальный заброс: " PerfColor " в " PerfX "," PerfY)
}

; ---- старт / стоп / выход -------------------------------------------------
F5:: {
    global Running
    if (BarColor = "") {
        Status("Сначала F6: курсор на красную кнопку полосы «Нажимайте быстро!»")
        return
    }
    Running := !Running
    if Running
        SetTimer FishLoop, -10
    else {
        Click "Up"
        Status("Стоп. Поймано: " Catches)
    }
}
F10:: {
    Click "Up"
    ExitApp
}

; ---- основной цикл --------------------------------------------------------
FishLoop() {
    global Running, Catches
    while Running {
        if !WaitRoblox()
            break

        ; 1. заброс: держим ЛКМ до полной шкалы (по F7) или CastHoldMs
        Status("Заброс… (поймано: " Catches ")")
        Click "Down"
        start := A_TickCount
        if (PerfColor != "") {
            Sleep 250                               ; шкала успевает появиться
            while Running && (A_TickCount - start < CastMaxHoldMs) && !Seen(PerfX, PerfY, PerfColor, 3)
                Sleep 15
        } else
            Sleep CastHoldMs
        Click "Up"
        if !Running
            break

        ; 2. ждём полосу «Нажимайте быстро!» — время поклёвки везде разное
        Status("Жду поклёвку… (поймано: " Catches ")")
        start := A_TickCount
        bitten := false
        while Running && (A_TickCount - start < BiteTimeoutS * 1000) {
            if !WaitRoblox()
                return
            if BarVisible() {
                bitten := true
                break
            }
            Sleep 30
        }
        if !Running
            break
        if !bitten {
            Status("Не клюнуло за " BiteTimeoutS " с — перезаброс")
            Tap()                                   ; сматываем
            Sleep 2000
            continue
        }

        ; 3. кликаем, пока полоса не заполнится и не исчезнет
        Status("Клюёт! Кликаю… (поймано: " Catches ")")
        start := A_TickCount
        goneSince := 0
        while Running && (A_TickCount - start < CatchMaxS * 1000) {
            if !WaitRoblox()
                return
            Tap()
            Sleep ClickEveryMs
            if BarVisible()
                goneSince := 0
            else if (goneSince = 0)
                goneSince := A_TickCount
            else if (A_TickCount - goneSince >= GoneMs)
                break
        }
        Catches += 1

        ; 4. улов показывается ~2-3 с — ждём и закидываем снова
        Status("Поймано: " Catches ". Пауза…")
        Sleep Random(CooldownMinMs, CooldownMaxMs)
    }
    Click "Up"
    Status("Стоп. Поймано: " Catches)
}

; ---- помощники ------------------------------------------------------------
Tap() {
    Click "Down"
    Sleep 15
    Click "Up"
}

BarVisible() => Seen(BarX, BarY, BarColor, 6)

; Есть ли цвет color в квадрате ±r пикселей вокруг точки.
Seen(x, y, color, r) => PixelSearch(&fx, &fy, x - r, y - r, x + r, y + r, color, Tolerance)

; Кликаем только в активном окне Roblox; иначе ждём, пока его вернут.
WaitRoblox() {
    global Running
    while Running && !WinActive(RobloxWin) {
        Click "Up"
        Status("Окно Roblox не активно — жду")
        Sleep 500
    }
    return Running
}

Cfg(section, key, def) => Integer(IniRead(Ini, section, key, def))

Status(text) {
    ToolTip "AFK-рыбалка: " text, 10, 10
}
