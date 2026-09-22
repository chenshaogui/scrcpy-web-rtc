# build.ps1 - Build scrcpy-webrtc image (Windows)
#
# Usage:
#   .\build.ps1                                       # local single-arch, --load
#   .\build.ps1 -MultiArch -Push -Registry "registry/path"   # multi-arch push
#   .\build.ps1 -MultiArch -Save                      # export multi-arch tar
#   .\build.ps1 -Save                                 # export single-arch tar (transfer to ARM server)
#
# Parameters:
#   -MultiArch   Build linux/amd64 + linux/arm64 (requires -Push / -Save)
#   -Push        Push to registry (required for multi-arch)
#   -Save        Export to tar file (for offline transfer)
#   -Registry    Registry prefix, e.g. "docker.io/username" or "ccr.ccs.tencentyun.com/ns"
#   -Tag         Image tag, default "latest"
#   -SkipVerify  Skip docker/buildx version check

[CmdletBinding()]
param(
    [switch]$MultiArch,
    [switch]$Push,
    [switch]$Save,
    [string]$Registry = "",
    [string]$Tag = "latest",
    [switch]$SkipVerify,
    [string]$BuildkitImage = "docker.io/moby/buildkit:buildx-stable-1"
)

$ErrorActionPreference = 'Stop'

# ---------- config ----------
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ImageName = "scrcpy-webrtc"
$NodeImage = "swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/node:22.23.2-slim"

# image full name
if ($Registry -ne "") {
    $FullImage = "${Registry}/${ImageName}:${Tag}"
} else {
    $FullImage = "${ImageName}:${Tag}"
}

# ---------- 1. verify buildx ----------
if (-not $SkipVerify) {
    Write-Host "[check] docker and buildx versions..." -ForegroundColor Cyan
    try {
        $dockerVer = (& docker --version) 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) { throw "docker command not available" }
        # SAFE: use string concatenation, never $var: in double-quoted strings
        Write-Host ("  docker : " + $dockerVer.Trim()) -ForegroundColor Green

        $buildxVer = (& docker buildx version) 2>&1 | Out-String
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] buildx not available. Install:" -ForegroundColor Red
            Write-Host "  https://docs.docker.com/build/install-buildx/" -ForegroundColor Yellow
            exit 1
        }
        Write-Host ("  buildx: " + $buildxVer.Trim()) -ForegroundColor Green
    } catch {
        Write-Host ("[ERROR] " + $_.Exception.Message) -ForegroundColor Red
        exit 1
    }
}

# ---------- 2. decide platforms ----------
if ($MultiArch) {
    $Platforms = "linux/amd64,linux/arm64"
} else {
    $arch = $env:PROCESSOR_ARCHITECTURE
    if ($arch -eq "ARM64" -or $arch -eq "aarch64") {
        $Platforms = "linux/arm64"
    } else {
        $Platforms = "linux/amd64"
    }
}

# ---------- 3. validate combos ----------
if ($Platforms -match ",") {
    if (-not $Push -and -not $Save) {
        Write-Host "[ERROR] -MultiArch requires -Push or -Save (buildx cannot --load multi-arch)." -ForegroundColor Red
        exit 1
    }
}

# ---------- 4. plan ----------
Write-Host ""
Write-Host "[plan] build plan:" -ForegroundColor Cyan
Write-Host ("  platform: " + $Platforms)
Write-Host ("  image   : " + $FullImage)
$action = if ($Push) { "push to registry" } elseif ($Save) { "export tar" } else { "load to local docker" }
Write-Host ("  action  : " + $action)
Write-Host ("  base img: " + $NodeImage)
Write-Host ""

# ---------- 5. ensure docker-container builder ----------
# The default 'docker' driver on Docker Desktop does NOT support the OCI
# exporter (needed for multi-arch tar export). Create a 'docker-container'
# builder which does, and use it via --builder so we don't change the global
# default for the user's other projects.
#
# The buildkit image is pulled from the CN mirror (Huawei Cloud, same source
# as the Node base image) so we don't hit Docker Hub's auth requirements.
# Pass `-BuildkitImage "docker.io/moby/buildkit:buildx-stable-1"` to override.
$BuilderName = "scrcpy-builder"

$builderList = (& docker buildx ls --format "{{.Name}}" 2>&1) | Out-String
# Always recreate so --driver-opt is applied (buildx has no --replace flag).
# Cost is ~1s; the previous builder's container is stopped automatically.
if ($builderList -match [regex]::Escape($BuilderName)) {
    Write-Host ("[setup] removing existing builder '" + $BuilderName + "' to refresh config...") -ForegroundColor Cyan
    & docker buildx rm $BuilderName 2>&1 | Out-Null
}
Write-Host ("[setup] creating docker-container builder '" + $BuilderName + "'...") -ForegroundColor Cyan
Write-Host ("  buildkit image: " + $BuildkitImage)
& docker buildx create --name $BuilderName --driver docker-container --driver-opt "image=$BuildkitImage" 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-Host "[ERROR] failed to create docker-container builder" -ForegroundColor Red
    exit 1
}

# ---------- 6. build ----------
Push-Location $ScriptDir
try {
    $commonArgs = @(
        "buildx", "build", "--builder", $BuilderName
        "--platform", $Platforms
        "--build-arg", "NODE_IMAGE=$NodeImage"
        "-t", $FullImage
    )

    if ($Push) {
        & docker @commonArgs "--push" "."
    } elseif ($Save) {
        $tarFile = Join-Path $ScriptDir "${ImageName}-${Tag}-$($Platforms.Replace('/', '-').Replace(',', '+')).tar"
        Write-Host ("[save] exporting to " + $tarFile) -ForegroundColor Cyan
        # type=oci supports manifest lists (multi-arch); type=docker does NOT
        # Docker 23+ can `docker load -i` an OCI tarball directly
        & docker @commonArgs "--output" "type=oci,dest=$tarFile" "."
    } else {
        & docker @commonArgs "--load" "."
    }

    # Check exit code FIRST, before printing success messages
    if ($LASTEXITCODE -ne 0) {
        Write-Host ("[FAIL] docker buildx build failed (exit " + $LASTEXITCODE + ")") -ForegroundColor Red
        exit $LASTEXITCODE
    }

    if ($Save) {
        Write-Host ""
        Write-Host ("[OK] exported: " + $tarFile) -ForegroundColor Green
        Write-Host "  transfer to ARM server, then:" -ForegroundColor Yellow
        Write-Host "    docker load -i <tar-file>" -ForegroundColor Yellow
        Write-Host ""
        Write-Host "  inspect the loaded image (multi-arch manifest list):" -ForegroundColor Yellow
        Write-Host "    docker manifest inspect <DockerId>" -ForegroundColor Yellow
    }

    Write-Host ""
    Write-Host ("[OK] build complete: " + $FullImage) -ForegroundColor Green

    if (-not $Push -and -not $Save) {
        Write-Host ("  verify: docker inspect " + $FullImage + " --format '{{.Architecture}}'") -ForegroundColor Yellow
    }
} finally {
    Pop-Location
}