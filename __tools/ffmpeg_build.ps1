# build-onefile-ffmpeg.ps1
# Minimal single-file FFmpeg for POSRelayd rdagent:
#   Includes scale/format filters for bgra -> yuv420p conversion required by libvpx
#   gdigrab desktop capture -> VP8 via libvpx -> IVF -> pipe
#
# Output:
#   One standalone ffmpeg.exe is copied next to this script.
#
# Important:
#   This script DOES NOT run pacman -Sy or pacman -Suy.
#   It does not refresh MSYS2 package databases.
#
# Note about LGPL:
#   This builds a statically linked FFmpeg executable. If you distribute it,
#   make sure your distribution process satisfies the LGPL requirements for FFmpeg.

$ErrorActionPreference = "Stop"

$ScriptDir  = Split-Path -Parent $MyInvocation.MyCommand.Path
$MsysRoot   = "C:\msys64"
$BashExe    = Join-Path $MsysRoot "usr\bin\bash.exe"

$WorkDir    = Join-Path $ScriptDir "_ffmpeg_build"
$OutDir     = $ScriptDir

$MsysUrl    = "https://github.com/msys2/msys2-installer/releases/latest/download/msys2-x86_64-latest.exe"
$MsysSetup  = Join-Path $WorkDir "msys2-x86_64-latest.exe"

$FfmpegVer  = "8.1.2"
$FfmpegUrl  = "https://ffmpeg.org/releases/ffmpeg-$FfmpegVer.tar.xz"
$FfmpegArc  = Join-Path $WorkDir "ffmpeg-$FfmpegVer.tar.xz"
$FfmpegDir  = Join-Path $WorkDir "ffmpeg-$FfmpegVer"
$InstallDir = Join-Path $WorkDir "install-onefile"

New-Item -ItemType Directory -Force -Path $WorkDir | Out-Null
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

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

$CheckScript   = Join-Path $WorkDir "00_check_packages.sh"
$ExtractScript = Join-Path $WorkDir "01_extract_ffmpeg.sh"
$BuildScript   = Join-Path $WorkDir "02_build_ffmpeg_onefile.sh"
$VerifyScript  = Join-Path $WorkDir "03_verify_onefile.sh"

Save-TextUtf8NoBom -Path $CheckScript -Text @'
#!/usr/bin/env bash
set -euo pipefail

echo "Checking required build tools without refreshing package databases..."

missing=0

for cmd in gcc make pkg-config nasm tar xz strip objdump; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing command: $cmd"
    missing=1
  fi
done

if ! pkg-config --exists vpx; then
  echo "missing pkg-config package: vpx"
  missing=1
fi

# For one-file output we need a real static libvpx archive, not only import DLL libs.
if ! pkg-config --static --libs vpx >/dev/null 2>&1; then
  echo "pkg-config cannot resolve static vpx libs"
  missing=1
fi

vpx_static_lib="$(pkg-config --variable=libdir vpx 2>/dev/null || true)/libvpx.a"
if [ ! -f "$vpx_static_lib" ]; then
  echo "missing static library: $vpx_static_lib"
  missing=1
fi

if [ "$missing" = "0" ]; then
  echo "All required build tools and static libraries are already installed."
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
echo "Re-checking required build tools and static libraries..."

missing=0

for cmd in gcc make pkg-config nasm tar xz strip objdump; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing command after install attempt: $cmd"
    missing=1
  fi
done

if ! pkg-config --exists vpx; then
  echo "missing pkg-config package after install attempt: vpx"
  missing=1
fi

if ! pkg-config --static --libs vpx >/dev/null 2>&1; then
  echo "pkg-config cannot resolve static vpx libs after install attempt"
  missing=1
fi

vpx_static_lib="$(pkg-config --variable=libdir vpx 2>/dev/null || true)/libvpx.a"
if [ ! -f "$vpx_static_lib" ]; then
  echo "missing static library after install attempt: $vpx_static_lib"
  missing=1
fi

if [ "$missing" != "0" ]; then
  echo ""
  echo "Required packages/static libraries are still missing." >&2
  echo "Install these packages manually in MSYS2 UCRT64, then run this script again:" >&2
  echo "  pacman -S --needed git make pkgconf diffutils tar xz mingw-w64-ucrt-x86_64-cc mingw-w64-ucrt-x86_64-binutils mingw-w64-ucrt-x86_64-pkgconf mingw-w64-ucrt-x86_64-nasm mingw-w64-ucrt-x86_64-libvpx" >&2
  exit 1
fi

echo "All required build tools and static libraries are installed."
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

# The key one-file switches are:
#   --enable-static --disable-shared
#   --pkg-config-flags=--static
#   --extra-ldexeflags=-static
# They force FFmpeg and libvpx to be linked into ffmpeg.exe instead of emitted as DLLs.
./configure \
  --prefix="$INSTALL_DIR" \
  --target-os=mingw32 \
  --arch=x86_64 \
  --enable-static \
  --disable-shared \
  --pkg-config-flags="--static" \
  --extra-ldflags="-static" \
  --extra-ldexeflags="-static" \
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
  --enable-libvpx \
  --enable-indev=gdigrab \
  --enable-decoder=bmp \
  --enable-decoder=rawvideo \
  --enable-encoder=libvpx_vp8 \
  --enable-filter=scale \
  --enable-filter=format \
  --enable-filter=null \
  --enable-swscale \
  --enable-muxer=ivf \
  --enable-protocol=pipe

echo ""
echo "Configured linkage:"
grep -E '^CONFIG_STATIC=' config.h || true
grep -E '^CONFIG_SHARED=' config.h || true

echo ""
echo "Configured programs:"
grep -E '^CONFIG_FFMPEG=' config.h || true

echo ""
echo "Configured libvpx encoder:"
grep -E 'CONFIG_LIBVPX.*ENCODER' config.h || true

 echo ""
echo "Configured filters/libs needed for bgra -> yuv420p:"
grep -E 'CONFIG_(SCALE|FORMAT|NULL)_FILTER|CONFIG_SWSCALE' config.h || true

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

exit 0
'@

Save-TextUtf8NoBom -Path $VerifyScript -Text @'
#!/usr/bin/env bash
set -euo pipefail

INSTALL_DIR="$(cygpath -u "$WIN_INSTALL_DIR")"
OUT_DIR="$(cygpath -u "$WIN_OUT_DIR")"

mkdir -p "$OUT_DIR"

if [ ! -f "$INSTALL_DIR/bin/ffmpeg.exe" ]; then
  echo "ffmpeg.exe was not found: $INSTALL_DIR/bin/ffmpeg.exe" >&2
  exit 1
fi

cp -f "$INSTALL_DIR/bin/ffmpeg.exe" "$OUT_DIR/ffmpeg.exe"

# Remove stale DLLs from older dynamic builds in this output directory.
# This is intentionally conservative: it removes only DLLs that are normally
# produced/copied by the previous FFmpeg build script.
rm -f "$OUT_DIR"/avcodec-*.dll \
      "$OUT_DIR"/avdevice-*.dll \
      "$OUT_DIR"/avfilter-*.dll \
      "$OUT_DIR"/avformat-*.dll \
      "$OUT_DIR"/avutil-*.dll \
      "$OUT_DIR"/postproc-*.dll \
      "$OUT_DIR"/swresample-*.dll \
      "$OUT_DIR"/swscale-*.dll \
      "$OUT_DIR"/libvpx*.dll \
      "$OUT_DIR"/libgcc_s_*.dll \
      "$OUT_DIR"/libstdc++-*.dll \
      "$OUT_DIR"/libwinpthread-*.dll 2>/dev/null || true

echo ""
echo "Checking imported DLLs for ffmpeg.exe..."

imports="$(objdump -p "$OUT_DIR/ffmpeg.exe" | sed -n 's/^[[:space:]]*DLL Name: //p' || true)"
printf '%s
' "$imports"

# For standalone distribution the real problem is importing MSYS2/MinGW/FFmpeg DLLs.
# Windows system DLLs are expected and are not files you ship next to ffmpeg.exe.
# This block intentionally fails only for known non-system runtime/media DLLs.
bad_imports="$(printf '%s
' "$imports" | grep -Ei '^(avcodec|avdevice|avfilter|avformat|avutil|postproc|swresample|swscale)-[0-9]+\.dll$|^lib(vpx|gcc|stdc\+\+|winpthread|bz2|brotli|iconv|intl|lzma|z|zstd|xml2|ssl|crypto).*\.dll$|^(zlib1|libzlib|libssp).*\.dll$' || true)"

if [ -n "$bad_imports" ]; then
  echo ""
  echo "ERROR: ffmpeg.exe still imports non-system MSYS2/FFmpeg DLLs:" >&2
  printf '%s
' "$bad_imports" >&2
  echo ""
  echo "This means one dependency was linked dynamically." >&2
  echo "Make sure MSYS2 UCRT64 has static libraries installed and configure used --pkg-config-flags=--static." >&2
  exit 1
fi

echo "One-file check passed: no FFmpeg/libvpx/MSYS2 DLL imports were detected."
echo "Note: Windows system DLL imports are normal and do not need to be bundled."
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

Write-Step "Building minimal one-file FFmpeg"
Invoke-MSYS2Script -ScriptPath $BuildScript

Write-Step "Copying and verifying standalone ffmpeg.exe"
Invoke-MSYS2Script -ScriptPath $VerifyScript

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
    Write-Host "Devices:"
    .\ffmpeg.exe -hide_banner -devices | Select-String "gdigrab"

    Write-Host ""
    Write-Host "Encoders:"
    .\ffmpeg.exe -hide_banner -encoders | Select-String "libvpx|vp8"

    Write-Host ""
    Write-Host "Muxers:"
    .\ffmpeg.exe -hide_banner -muxers | Select-String "ivf"

    Write-Host ""
    Write-Host "Protocols:"
    .\ffmpeg.exe -hide_banner -protocols | Select-String "pipe"

    Write-Host ""
    Write-Host "Filters:"
    .\ffmpeg.exe -hide_banner -filters | Select-String "scale|format"

    Write-Host ""
    Write-Host "Smoke test command:"
    Write-Host "  .\ffmpeg.exe -hide_banner -f gdigrab -draw_mouse 1 -framerate 1 -video_size 320x240 -i desktop -t 1 -an -pix_fmt yuv420p -c:v libvpx_vp8 -f ivf pipe:1 > NUL"
}
finally {
    Pop-Location
}

Write-Host ""
Write-Host "Done."
Write-Host ("Result standalone ffmpeg.exe: " + $ResultExe)
Write-Host "No FFmpeg/libvpx/MSYS2 DLL files should be required next to it."
Write-Host ""
Write-Host "Note:"
Write-Host "  If your Go project still uses '-c:v libvpx' and this FFmpeg build shows only 'libvpx_vp8',"
Write-Host "  change the project argument to '-c:v libvpx_vp8'."
