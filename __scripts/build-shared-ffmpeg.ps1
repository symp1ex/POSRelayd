# build-shared-ffmpeg.ps1
# Minimal shared/DLL FFmpeg build for POSRelayd rdagent:
#   Supports lavfi/ddagrab desktop capture and project encoders:
#   VP8 via libvpx_vp8 -> IVF -> pipe
#   H.264 via MediaFoundation h264_mf -> raw h264 -> pipe
#   AV1 via MediaFoundation av1_mf -> IVF -> pipe
#
# Output:
#   ffmpeg.exe and all non-system DLL dependencies are copied into:
#     .\ffmpeg-shared\
#
# Important:
#   This script DOES NOT run pacman -Sy or pacman -Suy.
#   It does not refresh MSYS2 package databases.
#
# Notes:
#   This is intentionally NOT a one-file/static build.
#   Keep the copied DLL files next to ffmpeg.exe when distributing/running it.

$ErrorActionPreference = "Stop"

$ScriptDir  = Split-Path -Parent $MyInvocation.MyCommand.Path
$MsysRoot   = "C:\msys64"
$BashExe    = Join-Path $MsysRoot "usr\bin\bash.exe"

$WorkDir    = Join-Path $ScriptDir "_ffmpeg_build"
$OutDir     = Join-Path $ScriptDir "ffmpeg-shared"

$MsysUrl    = "https://github.com/msys2/msys2-installer/releases/latest/download/msys2-x86_64-latest.exe"
$MsysSetup  = Join-Path $WorkDir "msys2-x86_64-latest.exe"

$FfmpegVer  = "8.1.2"
$FfmpegUrl  = "https://ffmpeg.org/releases/ffmpeg-$FfmpegVer.tar.xz"
$FfmpegArc  = Join-Path $WorkDir "ffmpeg-$FfmpegVer.tar.xz"
$FfmpegDir  = Join-Path $WorkDir "ffmpeg-$FfmpegVer"
$InstallDir = Join-Path $WorkDir "install-shared"

New-Item -ItemType Directory -Force -Path $WorkDir | Out-Null
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

function Write-Step {
    param([string]$Text)
    Write-Host ""
    Write-Host "==> $Text"
}

function Install-MSYS2-IfMissing {
    if (Test-Path $BashExe) {
        Write-Host "MSYS2 found: $BashExe"
        return
    }

    Write-Step "MSYS2 was not found. Downloading MSYS2 installer"

    if (-not (Test-Path $MsysSetup)) {
        Invoke-WebRequest -Uri $MsysUrl -OutFile $MsysSetup
    }

    Write-Step "Installing MSYS2 to $MsysRoot"

    if (Test-Path $MsysRoot) {
        throw "Directory exists but bash.exe was not found: $MsysRoot. Delete it completely or install MSYS2 manually."
    }

    $args = @(
        "install",
        "--root", $MsysRoot,
        "--confirm-command",
        "--accept-messages",
        "--accept-licenses"
    )

    $p = Start-Process -FilePath $MsysSetup -ArgumentList $args -Wait -PassThru

    if ($p.ExitCode -ne 0) {
        throw "MSYS2 installer failed with exit code $($p.ExitCode)"
    }

    if (-not (Test-Path $BashExe)) {
        throw "MSYS2 installation finished, but bash.exe was not found: $BashExe"
    }
}

function Invoke-MSYS2Script {
    param(
        [Parameter(Mandatory = $true)]
        [string]$ScriptPath
    )

    $env:MSYSTEM = "UCRT64"
    $env:CHERE_INVOKING = "1"

    $env:WIN_SCRIPT_DIR  = $ScriptDir
    $env:WIN_WORK_DIR    = $WorkDir
    $env:WIN_OUT_DIR     = $OutDir
    $env:WIN_FFMPEG_DIR  = $FfmpegDir
    $env:WIN_INSTALL_DIR = $InstallDir
    $env:WIN_FFMPEG_VER  = $FfmpegVer
    $env:WIN_MSYS_ROOT   = $MsysRoot

    & $BashExe --login $ScriptPath

    if ($LASTEXITCODE -ne 0) {
        throw "MSYS2 script failed with exit code $LASTEXITCODE"
    }
}

function Save-TextUtf8NoBom {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path,

        [Parameter(Mandatory = $true)]
        [string]$Text
    )

    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $Text, $utf8NoBom)
}

Install-MSYS2-IfMissing

Write-Step "Writing MSYS2 build scripts"

$CheckScript   = Join-Path $WorkDir "00_check_packages_shared.sh"
$ExtractScript = Join-Path $WorkDir "01_extract_ffmpeg_shared.sh"
$BuildScript   = Join-Path $WorkDir "02_build_ffmpeg_shared.sh"
$CopyScript    = Join-Path $WorkDir "03_copy_shared_runtime.sh"

Save-TextUtf8NoBom -Path $CheckScript -Text @'
#!/usr/bin/env bash
set -euo pipefail

echo "Checking required build tools without refreshing package databases..."

missing=0

for cmd in gcc make pkg-config nasm tar xz strip objdump ldd; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing command: $cmd"
    missing=1
  fi
done

if ! pkg-config --exists vpx; then
  echo "missing pkg-config package: vpx"
  missing=1
fi

if [ "$missing" = "0" ]; then
  echo "All required build tools and shared-library dependencies are already installed."
  exit 0
fi

echo ""
echo "Some required packages are missing."
echo "Trying to install them WITHOUT pacman -Sy and WITHOUT pacman -Suy..."
echo ""

pacman -S --needed --noconfirm \
  git make pkgconf diffutils tar xz \
  mingw-w64-ucrt-x86_64-cc \
  mingw-w64-ucrt-x86_64-binutils \
  mingw-w64-ucrt-x86_64-pkgconf \
  mingw-w64-ucrt-x86_64-nasm \
  mingw-w64-ucrt-x86_64-libvpx

echo ""
echo "Re-checking required build tools and shared-library dependencies..."

missing=0

for cmd in gcc make pkg-config nasm tar xz strip objdump ldd; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing command after install attempt: $cmd"
    missing=1
  fi
done

if ! pkg-config --exists vpx; then
  echo "missing pkg-config package after install attempt: vpx"
  missing=1
fi

if [ "$missing" != "0" ]; then
  echo ""
  echo "Required packages are still missing." >&2
  echo "Install these packages manually in MSYS2 UCRT64, then run this script again:" >&2
  echo "  pacman -S --needed git make pkgconf diffutils tar xz mingw-w64-ucrt-x86_64-cc mingw-w64-ucrt-x86_64-binutils mingw-w64-ucrt-x86_64-pkgconf mingw-w64-ucrt-x86_64-nasm mingw-w64-ucrt-x86_64-libvpx" >&2
  exit 1
fi

echo "All required build tools and shared-library dependencies are installed."
exit 0
'@

Save-TextUtf8NoBom -Path $ExtractScript -Text @'
#!/usr/bin/env bash
set -euo pipefail

WORK_DIR="$(cygpath -u "$WIN_WORK_DIR")"
FFMPEG_VER="${WIN_FFMPEG_VER}"
FFMPEG_ARC="$WORK_DIR/ffmpeg-$FFMPEG_VER.tar.xz"

cd "$WORK_DIR"

rm -rf "ffmpeg-$FFMPEG_VER"

if [ ! -f "$FFMPEG_ARC" ]; then
  echo "FFmpeg archive was not found: $FFMPEG_ARC" >&2
  exit 1
fi

tar -xf "$FFMPEG_ARC"

if [ ! -d "$WORK_DIR/ffmpeg-$FFMPEG_VER" ]; then
  echo "FFmpeg source directory was not created after extract" >&2
  exit 1
fi

exit 0
'@

Save-TextUtf8NoBom -Path $BuildScript -Text @'
#!/usr/bin/env bash
set -euo pipefail

SRC_DIR="$(cygpath -u "$WIN_FFMPEG_DIR")"
INSTALL_DIR="$(cygpath -u "$WIN_INSTALL_DIR")"

cd "$SRC_DIR"

make distclean >/dev/null 2>&1 || true
rm -rf "$INSTALL_DIR"
mkdir -p "$INSTALL_DIR"

# Shared/DLL build:
#   --enable-shared creates av*.dll/sw*.dll next to ffmpeg.exe during install.
#   --disable-static prevents everything from being packed into one executable.
#   No --pkg-config-flags=--static and no -static linker flags are used.
./configure \
  --prefix="$INSTALL_DIR" \
  --target-os=mingw32 \
  --arch=x86_64 \
  --disable-static \
  --enable-shared \
  --enable-gpl \
  --enable-gpl \
  --disable-autodetect \
  --disable-debug \
  --disable-doc \
  --disable-network \
  --enable-small \
  --disable-runtime-cpudetect \
  --disable-everything \
  --disable-ffplay \
  --disable-ffprobe \
  --enable-ffmpeg \
  --enable-d3d11va \
  --enable-dxva2 \
  --enable-mediafoundation \
  --enable-libvpx \
  --enable-indev=lavfi \
  --enable-filter=ddagrab \
  --enable-filter=hwdownload \
  --enable-filter=scale \
  --enable-filter=format \
  --enable-filter=null \
  --enable-swscale \
  --enable-decoder=wrapped_avframe \
  --enable-encoder=libvpx_vp8 \
  --enable-encoder=h264_mf \
  --enable-encoder=av1_mf \
  --enable-muxer=ivf \
  --enable-muxer=h264 \
  --enable-protocol=pipe

echo ""
echo "Configured linkage:"
grep -E '^CONFIG_STATIC=' config.h || true
grep -E '^CONFIG_SHARED=' config.h || true

echo ""
echo "Configured programs:"
grep -E '^CONFIG_FFMPEG=' config.h || true

echo ""
echo "Configured encoders:"
grep -E 'CONFIG_(LIBVPX_VP8|H264_MF|AV1_MF)_ENCODER' config.h || true

echo ""
echo "Configured filters/libs needed for capture and conversion:"
grep -E 'CONFIG_(DDAGRAB|HWDOWNLOAD|SCALE|FORMAT|NULL)_FILTER|CONFIG_SWSCALE|CONFIG_(D3D11VA|DXVA2|MEDIAFOUNDATION)' config.h || true

make -j"$(nproc)" V=0
make install

if [ ! -f "$INSTALL_DIR/bin/ffmpeg.exe" ]; then
  echo ""
  echo "ffmpeg.exe was not installed. Trying direct program build/install..."
  make ffmpeg.exe -j"$(nproc)" V=0
  mkdir -p "$INSTALL_DIR/bin"

  if [ -f "ffmpeg.exe" ]; then
    cp -f "ffmpeg.exe" "$INSTALL_DIR/bin/ffmpeg.exe"
  fi
fi

if [ ! -f "$INSTALL_DIR/bin/ffmpeg.exe" ]; then
  echo ""
  echo "ERROR: ffmpeg.exe was not produced." >&2
  echo "Check these lines from config.h:" >&2
  grep -E '^CONFIG_FFMPEG=' config.h >&2 || true
  grep -E 'CONFIG_LIBVPX.*ENCODER' config.h >&2 || true
  exit 1
fi

strip "$INSTALL_DIR/bin/ffmpeg.exe" 2>/dev/null || true
strip "$INSTALL_DIR/bin"/*.dll 2>/dev/null || true

exit 0
'@

Save-TextUtf8NoBom -Path $CopyScript -Text @'
#!/usr/bin/env bash
set -euo pipefail

INSTALL_DIR="$(cygpath -u "$WIN_INSTALL_DIR")"
OUT_DIR="$(cygpath -u "$WIN_OUT_DIR")"
UCRT_BIN="/ucrt64/bin"

mkdir -p "$OUT_DIR"
rm -f "$OUT_DIR"/*.exe "$OUT_DIR"/*.dll 2>/dev/null || true

if [ ! -f "$INSTALL_DIR/bin/ffmpeg.exe" ]; then
  echo "ffmpeg.exe was not found: $INSTALL_DIR/bin/ffmpeg.exe" >&2
  exit 1
fi

echo ""
echo "Copying FFmpeg executable and FFmpeg DLLs..."
cp -f "$INSTALL_DIR/bin/ffmpeg.exe" "$OUT_DIR/ffmpeg.exe"
cp -f "$INSTALL_DIR/bin"/*.dll "$OUT_DIR/" 2>/dev/null || true

is_system_dll() {
  local name
  name="$(printf '%s' "$1" | tr 'A-Z' 'a-z')"
  case "$name" in
    kernel32.dll|user32.dll|gdi32.dll|advapi32.dll|shell32.dll|ole32.dll|oleaut32.dll|uuid.dll|bcrypt.dll|secur32.dll|ws2_32.dll|mfplat.dll|mfuuid.dll|mf.dll|mfreadwrite.dll|strmiids.dll|d3d11.dll|dxgi.dll|dxva2.dll|dwmapi.dll|shlwapi.dll|setupapi.dll|cfgmgr32.dll|ntdll.dll|msvcrt.dll|ucrtbase.dll|api-ms-*.dll|ext-ms-*.dll)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

copy_one_dependency() {
  local dll
  local src
  dll="$1"

  if is_system_dll "$dll"; then
    return 0
  fi

  if [ -f "$OUT_DIR/$dll" ]; then
    return 0
  fi

  src=""
  if [ -f "$INSTALL_DIR/bin/$dll" ]; then
    src="$INSTALL_DIR/bin/$dll"
  elif [ -f "$UCRT_BIN/$dll" ]; then
    src="$UCRT_BIN/$dll"
  elif command -v "$dll" >/dev/null 2>&1; then
    src="$(command -v "$dll")"
  fi

  if [ -n "$src" ] && [ -f "$src" ]; then
    echo "  $dll"
    cp -f "$src" "$OUT_DIR/$dll"
  else
    echo "WARNING: could not find non-system DLL dependency: $dll" >&2
  fi
}

list_imports() {
  local file
  file="$1"
  objdump -p "$file" 2>/dev/null | sed -n 's/^[[:space:]]*DLL Name: //p' || true
}

echo ""
echo "Resolving and copying non-system DLL dependencies..."

# Iterate because copied DLLs can import other DLLs.
for pass in 1 2 3 4 5 6 7 8; do
  changed=0
  before_count="$(find "$OUT_DIR" -maxdepth 1 -type f \( -iname '*.exe' -o -iname '*.dll' \) | wc -l | tr -d ' ')"

  while IFS= read -r bin; do
    while IFS= read -r dll; do
      [ -n "$dll" ] || continue
      copy_one_dependency "$dll"
    done < <(list_imports "$bin")
  done < <(find "$OUT_DIR" -maxdepth 1 -type f \( -iname '*.exe' -o -iname '*.dll' \))

  after_count="$(find "$OUT_DIR" -maxdepth 1 -type f \( -iname '*.exe' -o -iname '*.dll' \) | wc -l | tr -d ' ')"
  if [ "$after_count" != "$before_count" ]; then
    changed=1
  fi

  if [ "$changed" = "0" ]; then
    break
  fi
done

echo ""
echo "Final files copied to output directory:"
find "$OUT_DIR" -maxdepth 1 -type f \( -iname '*.exe' -o -iname '*.dll' \) -printf '  %f\n' | sort

echo ""
echo "Checking for unresolved non-system DLL imports..."
unresolved=0

while IFS= read -r bin; do
  while IFS= read -r dll; do
    [ -n "$dll" ] || continue
    if is_system_dll "$dll"; then
      continue
    fi
    if [ ! -f "$OUT_DIR/$dll" ]; then
      echo "  Missing for $(basename "$bin"): $dll" >&2
      unresolved=1
    fi
  done < <(list_imports "$bin")
done < <(find "$OUT_DIR" -maxdepth 1 -type f \( -iname '*.exe' -o -iname '*.dll' \))

if [ "$unresolved" != "0" ]; then
  echo ""
  echo "ERROR: Some non-system DLL dependencies were not copied." >&2
  exit 1
fi

echo "All non-system DLL dependencies that objdump can see were copied."
echo "Windows system DLLs are expected to remain external."
exit 0
'@

Write-Step "Checking MSYS2 packages without database update"
Invoke-MSYS2Script -ScriptPath $CheckScript

Write-Step "Downloading FFmpeg source archive"

if (-not (Test-Path $FfmpegArc)) {
    Invoke-WebRequest -Uri $FfmpegUrl -OutFile $FfmpegArc
}

Write-Step "Extracting FFmpeg source archive inside MSYS2"
Invoke-MSYS2Script -ScriptPath $ExtractScript

if (-not (Test-Path $FfmpegDir)) {
    throw "FFmpeg source directory was not found after extract: $FfmpegDir"
}

Write-Step "Building minimal shared/DLL FFmpeg"
Invoke-MSYS2Script -ScriptPath $BuildScript

Write-Step "Copying ffmpeg.exe and required DLLs"
Invoke-MSYS2Script -ScriptPath $CopyScript

Write-Step "Verifying resulting binary"

$ResultExe = Join-Path $OutDir "ffmpeg.exe"

if (-not (Test-Path $ResultExe)) {
    throw "Result ffmpeg.exe was not found: $ResultExe"
}

Push-Location $OutDir
try {
    Write-Host ""
    Write-Host "Version:"
    .\ffmpeg.exe -hide_banner -version | Select-Object -First 5

    Write-Host ""
    Write-Host "Encoders:"
    .\ffmpeg.exe -hide_banner -encoders | Select-String "libvpx|vp8|h264_mf|av1_mf"

    Write-Host ""
    Write-Host "Muxers:"
    .\ffmpeg.exe -hide_banner -muxers | Select-String "ivf|h264"

    Write-Host ""
    Write-Host "Protocols:"
    .\ffmpeg.exe -hide_banner -protocols | Select-String "pipe"

    Write-Host ""
    Write-Host "Filters:"
    .\ffmpeg.exe -hide_banner -filters | Select-String "ddagrab|hwdownload|scale|format"

    Write-Host ""
    Write-Host "Decoders:"
    .\ffmpeg.exe -hide_banner -decoders | Select-String "wrapped_avframe"

    Write-Host ""
    Write-Host "Copied files:"
    Get-ChildItem -File | Sort-Object Name | Select-Object Name, Length | Format-Table -AutoSize

    Write-Host ""
    Write-Host "Smoke test commands:"
    Write-Host "  .\ffmpeg.exe -hide_banner -f lavfi -i ddagrab=framerate=1:video_size=320x240:draw_mouse=0 -t 1 -an -vf hwdownload,format=bgra -c:v libvpx_vp8 -f ivf pipe:1 > NUL"
    Write-Host "  .\ffmpeg.exe -hide_banner -f lavfi -i ddagrab=framerate=1:video_size=320x240:draw_mouse=0 -t 1 -an -vf hwdownload,format=bgra,format=nv12 -pix_fmt nv12 -c:v h264_mf -f h264 pipe:1 > NUL"
}
finally {
    Pop-Location
}

Write-Host ""
Write-Host "Done."
Write-Host ("Result shared/DLL FFmpeg directory: " + $OutDir)
Write-Host "Keep ffmpeg.exe and all copied DLL files together."
Write-Host "Windows MediaFoundation/D3D/system DLLs are expected and are not copied."
Write-Host ""
Write-Host "Note:"
Write-Host "  If your Go project still uses '-c:v libvpx' and this FFmpeg build shows only 'libvpx_vp8',"
Write-Host "  change the project argument to '-c:v libvpx_vp8'."
