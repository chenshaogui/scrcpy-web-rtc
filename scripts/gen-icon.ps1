# scripts/gen-icon.ps1 — draw the "云手机助手" app icon with GDI+.
# Concept: a vivid blue->cyan rounded-square badge, a bold white smartphone,
# and three broadcast waves radiating from the top (screen-cast / cloud phone).
# Corners are rounded with a transparent outside so fnOS's mask never shows
# white slivers at the four corners.
#
# Run:  powershell -NoProfile -ExecutionPolicy Bypass -File scripts/gen-icon.ps1

Add-Type -AssemblyName System.Drawing

$S = 1024   # master canvas size

function New-RoundedRect([single]$x, [single]$y, [single]$w, [single]$h, [single]$r) {
    $p = New-Object System.Drawing.Drawing2D.GraphicsPath
    $d = $r * 2
    $p.AddArc($x,          $y,          $d, $d, 180, 90)
    $p.AddArc($x + $w - $d, $y,          $d, $d, 270, 90)
    $p.AddArc($x + $w - $d, $y + $h - $d, $d, $d, 0,   90)
    $p.AddArc($x,          $y + $h - $d, $d, $d, 90,  90)
    $p.CloseFigure()
    return $p
}

function Draw-Icon([int]$size) {
    $bmp = New-Object System.Drawing.Bitmap($size, $size, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $g.Clear([System.Drawing.Color]::Transparent)

    $k = $size / $S   # scale factor from master coordinates

    # ── background: rounded square, diagonal blue -> cyan gradient ──
    $radius = 224 * $k
    $bgPath = New-RoundedRect 0 0 $size $size $radius
    $brush = New-Object System.Drawing.Drawing2D.LinearGradientBrush( `
        (New-Object System.Drawing.Rectangle(0, 0, $size, $size)), `
        [System.Drawing.Color]::FromArgb(255, 30, 107, 255), `
        [System.Drawing.Color]::FromArgb(255, 34, 211, 238), 45)
    # fill the gradient clipped to the rounded shape (corners stay transparent)
    $g.SetClip($bgPath)
    $g.FillRectangle($brush, 0, 0, $size, $size)
    $g.ResetClip()
    $bgPath.Dispose()

    # ── broadcast waves (screen-cast signal) radiating up from phone top ──
    $ox = 512 * $k            # wave origin x (phone top center)
    $oy = 380 * $k            # wave origin y
    $waveColor = [System.Drawing.Color]::FromArgb(255, 190, 240, 255)
    foreach ($r in 190, 130, 70) {
        $pen = New-Object System.Drawing.Pen($waveColor, (34 * $k))
        $pen.StartCap = [System.Drawing.Drawing2D.LineCap]::Round
        $pen.EndCap   = [System.Drawing.Drawing2D.LineCap]::Round
        $rr = $r * $k
        $g.DrawArc($pen, ($ox - $rr), ($oy - $rr), ($rr * 2), ($rr * 2), 180, 180)
        $pen.Dispose()
    }

    # ── phone body (white rounded rect) ──
    $phonePath = New-RoundedRect (372 * $k) (380 * $k) (280 * $k) (480 * $k) (48 * $k)
    $white = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::White)
    $g.FillPath($white, $phonePath)
    # soft outline so the white reads against light tiles too
    $op = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(70, 10, 40, 80), (2 * $k))
    $g.DrawPath($op, $phonePath)
    $op.Dispose()

    # ── phone screen (dark navy) ──
    $screenPath = New-RoundedRect (392 * $k) (400 * $k) (240 * $k) (440 * $k) (32 * $k)
    $navy = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(255, 14, 42, 71))
    $g.FillPath($navy, $screenPath)

    # ── cast glyph inside the screen: dot + two arcs (broadcast) ──
    $gx = 512 * $k
    $gy = 660 * $k
    $gc = [System.Drawing.Color]::FromArgb(255, 34, 211, 238)
    $dot = New-Object System.Drawing.SolidBrush($gc)
    $dr = 22 * $k
    $g.FillEllipse($dot, ($gx - $dr), ($gy - $dr), ($dr * 2), ($dr * 2))
    foreach ($r in 60, 108) {
        $pen = New-Object System.Drawing.Pen($gc, (20 * $k))
        $pen.StartCap = [System.Drawing.Drawing2D.LineCap]::Round
        $pen.EndCap   = [System.Drawing.Drawing2D.LineCap]::Round
        $rr = $r * $k
        $g.DrawArc($pen, ($gx - $rr), ($gy - $rr), ($rr * 2), ($rr * 2), 215, 130)
        $pen.Dispose()
    }
    $dot.Dispose()

    # ── home indicator bar ──
    $barPath = New-RoundedRect (482 * $k) (812 * $k) (60 * $k) (10 * $k) (5 * $k)
    $barBrush = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(160, 255, 255, 255))
    $g.FillPath($barBrush, $barPath)
    $barPath.Dispose(); $barBrush.Dispose()

    $phonePath.Dispose(); $screenPath.Dispose()
    $white.Dispose(); $navy.Dispose(); $brush.Dispose()
    $g.Dispose()
    return $bmp
}

$targets = @(
    @{ path = "fnos-app-native\package\app\ui\images\icon_256.png"; size = 256 },
    @{ path = "fnos-app-native\package\app\ui\images\icon_64.png";  size = 64  },
    @{ path = "fnos-app-native\package\ICON_256.PNG";              size = 256 },
    @{ path = "fnos-app-native\package\ICON.PNG";                  size = 64  }
)

foreach ($t in $targets) {
    $img = Draw-Icon $t.size
    $full = Join-Path (Get-Location) $t.path
    if (Test-Path $full) { Remove-Item $full -Force }
    $img.Save($full, [System.Drawing.Imaging.ImageFormat]::Png)
    $img.Dispose()
    Write-Output ("[OK] {0} ({1}px)" -f $t.path, $t.size)
}
Write-Output "icon regenerated."
