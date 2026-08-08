[CmdletBinding()]
param(
    [switch]$SkipFrontend
)

$ErrorActionPreference = 'Stop'
$repoRoot = $PSScriptRoot
$frontendDir = Join-Path $repoRoot 'frontend'
$backendDir = Join-Path $repoRoot 'backend'
$webIndex = Join-Path $backendDir 'internal\web\dist\index.html'
$testDir = Join-Path (Split-Path $repoRoot -Parent) 'sub2api-guardian-test'
$dataDir = Join-Path $testDir 'data'
$target = Join-Path $testDir 'guardian.exe'
$temporaryTarget = Join-Path $testDir ('.guardian.exe.{0}.tmp' -f $PID)
$buildCache = Join-Path $testDir '.go-build-cache'

if (-not $SkipFrontend) {
    Write-Host 'Building frontend...'
    & pnpm --dir $frontendDir run build
    if ($LASTEXITCODE -ne 0) {
        throw "Frontend build failed with exit code $LASTEXITCODE."
    }
} elseif (-not (Test-Path -LiteralPath $webIndex -PathType Leaf)) {
    throw 'The embedded frontend is missing. Run again without -SkipFrontend.'
}

New-Item -ItemType Directory -Force -Path $testDir | Out-Null
New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
New-Item -ItemType Directory -Force -Path $buildCache | Out-Null

$savedEnv = @{
    GOOS = $env:GOOS
    GOARCH = $env:GOARCH
    CGO_ENABLED = $env:CGO_ENABLED
    GOCACHE = $env:GOCACHE
}

try {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    $env:GOCACHE = $buildCache
    Write-Host 'Building Windows AMD64 executable...'
    Push-Location $backendDir
    try {
        & go build -buildvcs=false -trimpath -ldflags '-s -w' -o $temporaryTarget ./cmd/guardian
        if ($LASTEXITCODE -ne 0) {
            throw "Go build failed with exit code $LASTEXITCODE."
        }
    } finally {
        Pop-Location
    }

    try {
        Move-Item -LiteralPath $temporaryTarget -Destination $target -Force
    } catch {
        throw "Unable to replace $target. Stop the running guardian.exe and run the script again. $($_.Exception.Message)"
    }
} finally {
    foreach ($name in $savedEnv.Keys) {
        $value = $savedEnv[$name]
        if ($null -eq $value) {
            Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
        } else {
            Set-Item -Path "Env:$name" -Value $value
        }
    }
    if (Test-Path -LiteralPath $temporaryTarget) {
        Remove-Item -LiteralPath $temporaryTarget -Force
    }
	if (Test-Path -LiteralPath $buildCache) {
		Remove-Item -LiteralPath $buildCache -Recurse -Force
	}
}

$startScript = @'
@echo off
setlocal

if "%GUARDIAN_PORT%"=="" set "GUARDIAN_PORT=8787"
set "GUARDIAN_ADDR=127.0.0.1:%GUARDIAN_PORT%"
set "GUARDIAN_DATA_DIR=%~dp0data"

if not exist "%~dp0guardian.exe" (
  echo guardian.exe not found. Run sub2api-guardian\build-windows-test.ps1 first.
  pause
  exit /b 1
)

start "Sub2API Guardian" /D "%~dp0" "%~dp0guardian.exe"
timeout /t 2 /nobreak >nul
start "" "http://127.0.0.1:%GUARDIAN_PORT%"
'@
[IO.File]::WriteAllText((Join-Path $testDir 'start-guardian.cmd'), $startScript, [Text.UTF8Encoding]::new($false))

$readme = @'
Sub2API Guardian Windows 测试目录

1. 双击 start-guardian.cmd 启动。
2. 浏览器默认打开 http://127.0.0.1:8787。
3. 关闭标题为 Sub2API Guardian 的命令行窗口即可停止服务。
4. data 目录保存本地测试数据库，更新 guardian.exe 时不会删除。

重新打包：
powershell -NoProfile -ExecutionPolicy Bypass -File ..\sub2api-guardian\build-windows-test.ps1

自定义端口示例：
set GUARDIAN_PORT=9090 && start-guardian.cmd
'@
[IO.File]::WriteAllText((Join-Path $testDir 'README.txt'), $readme, [Text.UTF8Encoding]::new($false))

$package = Get-Content -LiteralPath (Join-Path $frontendDir 'package.json') -Raw | ConvertFrom-Json
$checksum = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant()
$buildInfo = @(
    "version=$($package.version)"
    "built_at=$([DateTimeOffset]::Now.ToString('yyyy-MM-dd HH:mm:ss zzz'))"
    'target=windows/amd64'
    "sha256=$checksum"
) -join "`n"
[IO.File]::WriteAllText((Join-Path $testDir 'build-info.txt'), $buildInfo + "`n", [Text.UTF8Encoding]::new($false))

Write-Host "Windows test package: $testDir"
Write-Host "SHA-256: $checksum"
Write-Host 'The existing data directory was preserved.'
