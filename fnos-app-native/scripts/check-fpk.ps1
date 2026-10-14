$f = 'd:/myProject/scrcpy-web-rtc/fnos-app-native/dist/scrcpy-webrtc_0.1.2_arm.fpk'
$t = Join-Path $env:TEMP 'fpkchk'
if (Test-Path $t) { Remove-Item $t -Recurse -Force }
New-Item -ItemType Directory $t | Out-Null
Copy-Item $f (Join-Path $t 'a.zip')
Expand-Archive (Join-Path $t 'a.zip') (Join-Path $t 'x')
Get-ChildItem (Join-Path $t 'x') -Recurse -File | ForEach-Object { Write-Output $_.FullName.Replace("$t\x", '') }
$p = Get-ChildItem (Join-Path $t 'x') -Recurse -Filter proxy.cjs | Select-Object -First 1
Write-Output '---- proxy.cjs tail ----'
Get-Content $p.FullName -Tail 12
