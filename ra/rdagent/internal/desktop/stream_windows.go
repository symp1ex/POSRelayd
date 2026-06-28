package desktop

import (
	"bufio"
	"context"
	"fmt"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	"io"
	"os"
	"path/filepath"
	"rdagent/internal/config"
	"rdagent/internal/screen"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"

	"rdagent/internal/logger"
	"rdagent/internal/winsta"
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	monitorDefaultToNearest = 2
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")

	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procMonitorFromWindow   = user32.NewProc("MonitorFromWindow")
	procMonitorFromPoint    = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
)

type winPoint struct {
	X int32
	Y int32
}

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type monitorInfo struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
}

type Geometry struct {
	X      int
	Y      int
	Width  int
	Height int
}

type Profile struct {
	FPS         int
	BitrateKbps int
	MaxrateKbps int
	BufsizeKbps int

	// CRF включает constrained-quality поведение вместе с maxrate/bufsize.
	// Для desktop-контента это лучше, чем жить только на target bitrate:
	// текст/границы UI сохраняются лучше, а простые сцены сами снижают средний bitrate.
	CRF int

	// static-thresh позволяет libvpx пропускать почти неизменившиеся блоки.
	// Для рабочего стола с большими статичными областями это снижает CPU/bitrate.
	StaticThresh int
}

type Stream struct {
	sessionID string
	track     *webrtc.TrackLocalStaticSample
	sender    *webrtc.RTPSender
	cfg       config.Config

	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	ffmpeg      *ffmpegProcess
	profile     Profile
	geom        Geometry
	desktopName string
	lastFrameAt time.Time

	rembBps atomic.Uint64
	pliCnt  atomic.Uint64
	nackCnt atomic.Uint64

	keyframeReqCh     chan string
	lastKeyframeForce time.Time
}

func NewStream(sessionID string, track *webrtc.TrackLocalStaticSample, sender *webrtc.RTPSender, cfg config.Config) (*Stream, error) {
	return &Stream{
		sessionID: sessionID,
		track:     track,
		sender:    sender,
		cfg:       cfg,

		// Не стартуем с ultra: это создаёт CPU/network spike в начале сессии.
		// Более высокий профиль будет выбран позже только после устойчивой стабильности.
		profile: mediumProfile24(),

		keyframeReqCh: make(chan string, 1),
	}, nil
}

//func initialProfileForGeometry(g Geometry) Profile {
//	// TEST ONLY: fixed profile.
//	return lowProfile24()
//}

func initialProfileForGeometry(g Geometry) Profile {
	pixels := g.Width * g.Height

	switch {
	case pixels <= 1280*720:
		return mediumProfile24()
	case pixels <= 1920*1080:
		return mediumProfile24()
	case pixels <= 2560*1440:
		return highProfile24()
	default:
		// Даже для 4K/ultrawide не стартуем с ultra.
		// Ultra можно получить позже только через стабильный upgrade.
		return highProfile24()
	}
}

func (s *Stream) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		return nil
	}

	geom, err := currentGeometry()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	s.geom = geom

	// Стартовый профиль зависит от реальной площади захвата.
	// Это дешевле, чем всегда стартовать с ultra, особенно на 4K/ultrawide.
	s.profile = initialProfileForGeometry(geom)

	if err := s.startFFmpegLocked(); err != nil {
		cancel()
		s.ctx = nil
		s.cancel = nil
		return err
	}

	screen.SetCaptureGeometry(screen.Geometry{
		X:      s.geom.X,
		Y:      s.geom.Y,
		Width:  s.geom.Width,
		Height: s.geom.Height,
	})

	go s.readRTCP(ctx)
	go s.watchKeyframeRequests(ctx)
	go s.watchGeometry(ctx)
	go s.watchDesktop(ctx)
	go s.watchFrameStall(ctx)
	go s.watchAdaptation(ctx)

	logger.RDAgent.Infof("Desktop stream started: %dx%d @ %dfps desktop=%s",
		s.geom.Width, s.geom.Height, s.profile.FPS, s.desktopName)

	return nil
}

func (s *Stream) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
		s.ctx = nil
	}

	s.stopFFmpegLocked()
	screen.ClearCaptureGeometry()
	logger.RDAgent.Info("Desktop stream stopped")
}

func currentGeometry() (Geometry, error) {
	if geom, ok := activeMonitorGeometry(); ok {
		return geom, nil
	}

	// Fallback: старое поведение, если WinAPI не смог определить монитор.
	x, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	y, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	w, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	h, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)

	geom := Geometry{
		X:      int(x),
		Y:      int(y),
		Width:  int(w),
		Height: int(h),
	}

	if geom.Width <= 0 || geom.Height <= 0 {
		return Geometry{}, fmt.Errorf("invalid desktop geometry: %+v", geom)
	}

	return geom, nil
}

func activeMonitorGeometry() (Geometry, bool) {
	// 1. Предпочитаем монитор активного окна.
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd != 0 {
		monitor, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
		if geom, ok := monitorGeometry(monitor); ok {
			return geom, true
		}
	}

	// 2. Fallback: монитор под курсором.
	var pt winPoint
	if ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ret != 0 {
		// POINT передаётся в MonitorFromPoint как два int32, упакованные в uintptr.
		point := uintptr(uint32(pt.X)) | (uintptr(uint32(pt.Y)) << 32)
		monitor, _, _ := procMonitorFromPoint.Call(point, monitorDefaultToNearest)
		if geom, ok := monitorGeometry(monitor); ok {
			return geom, true
		}
	}

	return Geometry{}, false
}

func monitorGeometry(monitor uintptr) (Geometry, bool) {
	if monitor == 0 {
		return Geometry{}, false
	}

	info := monitorInfo{
		CbSize: uint32(unsafe.Sizeof(monitorInfo{})),
	}

	ret, _, _ := procGetMonitorInfoW.Call(
		monitor,
		uintptr(unsafe.Pointer(&info)),
	)
	if ret == 0 {
		return Geometry{}, false
	}

	width := int(info.RcMonitor.Right - info.RcMonitor.Left)
	height := int(info.RcMonitor.Bottom - info.RcMonitor.Top)
	if width <= 0 || height <= 0 {
		return Geometry{}, false
	}

	return Geometry{
		X:      int(info.RcMonitor.Left),
		Y:      int(info.RcMonitor.Top),
		Width:  width,
		Height: height,
	}, true
}
func (s *Stream) ffmpegArgs() []string {
	switch s.cfg.VideoEncoder {
	case "h264_mf":
		return s.ffmpegArgsH264MF()
	case "av1_mf":
		return s.ffmpegArgsAV1MF()
	case "libvpx":
		fallthrough
	default:
		return s.ffmpegArgsVP8Libvpx()
	}
}

func (s *Stream) ffmpegArgsH264MF() []string {
	p := s.profile

	args := s.ffmpegInputArgs()

	args = append(args,
		"-vf", "hwdownload,format=bgra,format=nv12",
		"-pix_fmt", "nv12",

		"-c:v", "h264_mf",
		"-scenario", s.cfg.MFScenario,
		"-hw_encoding", boolToFFmpegInt(s.cfg.MFHWEncoding),

		"-g", strconv.Itoa(p.FPS),
		"-b:v", fmt.Sprintf("%dk", p.BitrateKbps),

		"-f", "h264",
		"pipe:1",
	)

	return args
}

func (s *Stream) ffmpegInputArgs() []string {
	g := s.geom
	p := s.profile

	source := fmt.Sprintf(
		"ddagrab=framerate=%d:video_size=%dx%d:offset_x=%d:offset_y=%d:draw_mouse=0:dup_frames=1",
		p.FPS,
		g.Width,
		g.Height,
		g.X,
		g.Y,
	)

	return []string{
		"-hide_banner",
		"-loglevel", "warning",

		"-f", "lavfi",
		"-i", source,

		"-an",
	}
}

func (s *Stream) ffmpegArgsVP8Libvpx() []string {
	p := s.profile

	args := s.ffmpegInputArgs()

	args = append(args,
		"-c:v", "libvpx",
		"-vf", "hwdownload,format=bgra",
		"-deadline", "realtime",
		"-cpu-used", "8",
		"-lag-in-frames", "0",
		"-error-resilient", "1",
		"-auto-alt-ref", "0",
		"-quality", "realtime",

		"-g", strconv.Itoa(p.FPS*2),
		"-keyint_min", strconv.Itoa(p.FPS),

		// Constrained-quality: CRF задаёт желаемое качество,
		// maxrate/bufsize ограничивают пики bitrate.
		"-crf", strconv.Itoa(p.CRF),
		"-b:v", fmt.Sprintf("%dk", p.BitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", p.MaxrateKbps),
		"-bufsize", fmt.Sprintf("%dk", p.BufsizeKbps),

		// Оптимизация для почти статичного desktop-контента.
		"-static-thresh", strconv.Itoa(p.StaticThresh),

		"-f", "ivf",
		"pipe:1",
	)

	return args
}

func (s *Stream) ffmpegArgsAV1MF() []string {
	p := s.profile

	args := s.ffmpegInputArgs()

	args = append(args,
		// gdigrab отдает RGB/BGRA-like frames, а MediaFoundation AV1 encoder
		// обычно не принимает их напрямую. NV12 — самый совместимый формат
		// для hardware video encoders на Windows.
		"-vf", "hwdownload,format=bgra,format=nv12",
		"-pix_fmt", "nv12",

		"-c:v", "av1_mf",

		// Эти опции зависят от конкретной сборки FFmpeg и драйвера.
		// Перед включением в production проверить:
		// ffmpeg -hide_banner -h encoder=av1_mf
		"-hw_encoding", boolToFFmpegInt(s.cfg.MFHWEncoding),

		"-g", strconv.Itoa(p.FPS*2),
		"-b:v", fmt.Sprintf("%dk", p.BitrateKbps),

		"-f", "ivf",
		"pipe:1",
	)

	return args
}

func boolToFFmpegInt(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func ffmpegPath() string {
	exePath, err := os.Executable()
	if err == nil {
		local := filepath.Join(filepath.Dir(exePath), "ffmpeg.exe")
		if _, statErr := os.Stat(local); statErr == nil {
			return local
		}
	}

	if wd, err := os.Getwd(); err == nil {
		local := filepath.Join(wd, "ffmpeg.exe")
		if _, statErr := os.Stat(local); statErr == nil {
			return local
		}
	}

	return "ffmpeg"
}

type ffmpegProcess struct {
	hProcess windows.Handle
	hThread  windows.Handle
	stdout   *os.File
	stderr   *os.File
}

func (p *ffmpegProcess) KillAndWait() {
	if p == nil {
		return
	}

	if p.hProcess != 0 {
		_ = windows.TerminateProcess(p.hProcess, 1)
		_, _ = windows.WaitForSingleObject(p.hProcess, 5000)
	}

	if p.stdout != nil {
		_ = p.stdout.Close()
		p.stdout = nil
	}

	if p.stderr != nil {
		_ = p.stderr.Close()
		p.stderr = nil
	}

	if p.hThread != 0 {
		_ = windows.CloseHandle(p.hThread)
		p.hThread = 0
	}

	if p.hProcess != 0 {
		_ = windows.CloseHandle(p.hProcess)
		p.hProcess = 0
	}
}

func startFFmpegOnDesktop(
	ctx context.Context,
	bin string,
	args []string,
	desktopFullName string,
) (*ffmpegProcess, io.ReadCloser, io.ReadCloser, error) {
	stdoutR, stdoutW, err := makeInheritablePipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderrR, stderrW, err := makeInheritablePipe()
	if err != nil {
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stdoutW)
		return nil, nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}

	cmdline := commandLine(bin, args)

	si := new(windows.StartupInfo)
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	si.ShowWindow = windows.SW_HIDE
	si.StdOutput = stdoutW
	si.StdErr = stderrW
	si.StdInput = windows.InvalidHandle
	si.Desktop = windows.StringToUTF16Ptr(desktopFullName)

	var pi windows.ProcessInformation

	creationFlags := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP)

	err = windows.CreateProcess(
		nil,
		windows.StringToUTF16Ptr(cmdline),
		nil,
		nil,
		true,
		creationFlags,
		nil,
		nil,
		si,
		&pi,
	)

	_ = windows.CloseHandle(stdoutW)
	_ = windows.CloseHandle(stderrW)

	if err != nil {
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stderrR)
		return nil, nil, nil, fmt.Errorf("CreateProcess desktop=%s failed: %w", desktopFullName, err)
	}

	stdoutFile := os.NewFile(uintptr(stdoutR), "ffmpeg-stdout")
	stderrFile := os.NewFile(uintptr(stderrR), "ffmpeg-stderr")

	proc := &ffmpegProcess{
		hProcess: pi.Process,
		hThread:  pi.Thread,
		stdout:   stdoutFile,
		stderr:   stderrFile,
	}

	go func() {
		<-ctx.Done()
		proc.KillAndWait()
	}()

	return proc, stdoutFile, stderrFile, nil
}

func makeInheritablePipe() (windows.Handle, windows.Handle, error) {
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1

	var read windows.Handle
	var write windows.Handle

	if err := windows.CreatePipe(&read, &write, &sa, 0); err != nil {
		return 0, 0, err
	}

	if err := windows.SetHandleInformation(read, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		_ = windows.CloseHandle(read)
		_ = windows.CloseHandle(write)
		return 0, 0, err
	}

	return read, write, nil
}

func commandLine(bin string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteWindowsArg(bin))

	for _, arg := range args {
		parts = append(parts, quoteWindowsArg(arg))
	}

	return strings.Join(parts, " ")
}

func quoteWindowsArg(s string) string {
	if s == "" {
		return `""`
	}

	needsQuote := strings.ContainsAny(s, " \t\n\v\"")
	if !needsQuote {
		return s
	}

	var b strings.Builder
	b.WriteByte('"')

	backslashes := 0
	for _, r := range s {
		if r == '\\' {
			backslashes++
			continue
		}

		if r == '"' {
			b.WriteString(strings.Repeat("\\", backslashes*2+1))
			b.WriteRune(r)
			backslashes = 0
			continue
		}

		if backslashes > 0 {
			b.WriteString(strings.Repeat("\\", backslashes))
			backslashes = 0
		}

		b.WriteRune(r)
	}

	if backslashes > 0 {
		b.WriteString(strings.Repeat("\\", backslashes*2))
	}

	b.WriteByte('"')
	return b.String()
}

func (s *Stream) startFFmpegLocked() error {
	args := s.ffmpegArgs()
	bin := ffmpegPath()

	desktop, err := winsta.BindCurrentThreadToInputDesktop("ffmpeg-start")
	if err != nil {
		return fmt.Errorf("bind ffmpeg thread to input desktop: %w", err)
	}
	defer desktop.Close()

	s.desktopName = desktop.Name()
	s.lastFrameAt = time.Now()

	desktopFullName := desktop.FullName()

	proc, stdout, stderr, err := startFFmpegOnDesktop(
		s.ctx,
		bin,
		args,
		desktopFullName,
	)
	if err != nil {
		return err
	}

	s.ffmpeg = proc

	logger.RDAgent.Infof(
		"FFmpeg process started on desktop: name=%s full=%s",
		desktop.Name(),
		desktopFullName,
	)

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			logger.RDAgent.Debugf("[ffmpeg] %s", scanner.Text())
		}
	}()

	go s.forwardEncodedVideo(stdout, s.profile)

	logger.RDAgent.Infof(
		"FFmpeg started: encoder=%s codec=%s command=%s %s",
		s.cfg.VideoEncoder,
		s.cfg.VideoCodec,
		bin,
		strings.Join(args, " "),
	)
	return nil
}

func (s *Stream) forwardEncodedVideo(stdout io.Reader, profile Profile) {
	switch s.cfg.VideoEncoder {
	case "h264_mf":
		s.forwardH264AnnexB(stdout, profile)

	case "av1_mf":
		s.forwardIVF(stdout, profile)

	case "libvpx":
		fallthrough
	default:
		s.forwardIVF(stdout, profile)
	}
}

func (s *Stream) forwardIVF(stdout io.Reader, profile Profile) {
	ivf, _, err := ivfreader.NewWith(stdout)
	if err != nil {
		logger.RDAgent.Errorf("ivf reader create failed: %v", err)
		return
	}

	frameDuration := time.Second / time.Duration(profile.FPS)
	frameCount := 0
	lastLog := time.Now()

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		frame, _, err := ivf.ParseNextFrame()
		if err != nil {
			if err != io.EOF && s.ctx.Err() == nil {
				logger.RDAgent.Errorf("read IVF video frame stopped: codec=%s error=%v", s.cfg.VideoCodec, err)
			}
			return
		}

		s.markFrameSent()

		frameCount++
		if time.Since(lastLog) >= 2*time.Second {
			logger.RDAgent.Debugf("%s IVF frames sent: %d in last 2s", s.cfg.VideoCodec, frameCount)
			frameCount = 0
			lastLog = time.Now()
		}

		if err := s.track.WriteSample(media.Sample{
			Data:     frame,
			Duration: frameDuration,
		}); err != nil && err != io.ErrClosedPipe {
			logger.RDAgent.Errorf("write IVF video sample failed: %v", err)
			return
		}
	}
}

type h264AccessUnitReader struct {
	reader  *h264reader.H264Reader
	pending *h264reader.NAL
}

func newH264AccessUnitReader(r io.Reader) (*h264AccessUnitReader, error) {
	reader, err := h264reader.NewReader(r)
	if err != nil {
		return nil, err
	}

	return &h264AccessUnitReader{
		reader: reader,
	}, nil
}

func (r *h264AccessUnitReader) ReadAccessUnit() ([]byte, error) {
	var accessUnit []byte
	hasVCL := false

	for {
		nal := r.pending
		r.pending = nil

		if nal == nil {
			next, err := r.reader.NextNAL()
			if err != nil {
				if err == io.EOF && len(accessUnit) > 0 {
					return accessUnit, nil
				}
				return nil, err
			}

			nal = next
		}

		if nal == nil || len(nal.Data) == 0 {
			continue
		}

		nalType := nal.Data[0] & 0x1F
		isVCL := nalType == 1 || nalType == 5

		if isVCL && hasVCL {
			r.pending = nal
			return accessUnit, nil
		}

		accessUnit = appendAnnexBNAL(accessUnit, nal.Data)

		if isVCL {
			hasVCL = true
		}
	}
}

func appendAnnexBNAL(dst []byte, nal []byte) []byte {
	dst = append(dst, 0x00, 0x00, 0x00, 0x01)
	dst = append(dst, nal...)
	return dst
}

func (s *Stream) forwardH264AnnexB(stdout io.Reader, profile Profile) {
	reader, err := newH264AccessUnitReader(stdout)
	if err != nil {
		logger.RDAgent.Errorf("h264 reader create failed: %v", err)
		return
	}

	frameDuration := time.Second / time.Duration(profile.FPS)
	frameCount := 0
	lastLog := time.Now()

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		accessUnit, err := reader.ReadAccessUnit()
		if err != nil {
			if err != io.EOF && s.ctx.Err() == nil {
				logger.RDAgent.Errorf("read H264 access unit stopped: %v", err)
			}
			return
		}

		if len(accessUnit) == 0 {
			continue
		}

		s.markFrameSent()

		frameCount++
		if time.Since(lastLog) >= 2*time.Second {
			logger.RDAgent.Debugf("H264 access units sent: %d in last 2s", frameCount)
			frameCount = 0
			lastLog = time.Now()
		}

		if err := s.track.WriteSample(media.Sample{
			Data:     accessUnit,
			Duration: frameDuration,
		}); err != nil && err != io.ErrClosedPipe {
			logger.RDAgent.Errorf("write H264 video sample failed: %v", err)
			return
		}
	}
}

func (s *Stream) markFrameSent() {
	s.mu.Lock()
	s.lastFrameAt = time.Now()
	s.mu.Unlock()
}

func (s *Stream) stopFFmpegLocked() {
	if s.ffmpeg == nil {
		return
	}

	s.ffmpeg.KillAndWait()
	s.ffmpeg = nil
}

func (s *Stream) restart(reason string, newGeom Geometry, newProfile Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ctx == nil {
		return
	}

	logger.RDAgent.Infof(
		"Restart desktop encoder: reason=%s old_desktop=%s",
		reason,
		s.desktopName,
	)

	s.stopFFmpegLocked()
	s.geom = newGeom
	s.profile = newProfile
	s.lastFrameAt = time.Now()

	if err := s.startFFmpegLocked(); err != nil {
		logger.RDAgent.Errorf("restart ffmpeg failed: %v", err)
		return
	}

	screen.SetCaptureGeometry(screen.Geometry{
		X:      s.geom.X,
		Y:      s.geom.Y,
		Width:  s.geom.Width,
		Height: s.geom.Height,
	})
}

func sameGeometry(a, b Geometry) bool {
	return a.X == b.X &&
		a.Y == b.Y &&
		a.Width == b.Width &&
		a.Height == b.Height
}

func (s *Stream) watchGeometry(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			geom, err := currentGeometry()
			if err != nil {
				continue
			}

			s.mu.Lock()
			old := s.geom
			profile := s.profile
			s.mu.Unlock()

			if !sameGeometry(geom, old) {
				s.restart("desktop geometry changed", geom, profile)
			}
		}
	}
}

func (s *Stream) watchDesktop(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	lastRestart := time.Time{}

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			name, err := winsta.CurrentInputDesktopName()
			if err != nil {
				logger.RDAgent.Debugf("Current input desktop check failed: %v", err)
				continue
			}

			s.mu.Lock()
			oldName := s.desktopName
			geom := s.geom
			profile := s.profile
			s.mu.Unlock()

			if name == "" || name == "unknown" || name == oldName {
				continue
			}

			if time.Since(lastRestart) < 1*time.Second {
				continue
			}
			lastRestart = time.Now()

			logger.RDAgent.Infof(
				"Input desktop changed: old=%s new=%s new_full=WinSta0\\%s, restarting ffmpeg",
				oldName,
				name,
				name,
			)

			s.restart("input desktop changed "+oldName+" -> "+name, geom, profile)
		}
	}
}

func (s *Stream) watchFrameStall(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	lastRestart := time.Time{}

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			s.mu.Lock()
			lastFrameAt := s.lastFrameAt
			geom := s.geom
			profile := s.profile
			desktopName := s.desktopName
			s.mu.Unlock()

			if lastFrameAt.IsZero() {
				continue
			}

			stalledFor := time.Since(lastFrameAt)
			if stalledFor < 3*time.Second {
				continue
			}

			if time.Since(lastRestart) < 5*time.Second {
				continue
			}
			lastRestart = time.Now()

			logger.RDAgent.Warnf(
				"Desktop stream stalled: desktop=%s stalled_for=%s, restarting ffmpeg",
				desktopName,
				stalledFor,
			)

			s.restart("desktop stream stalled", geom, profile)
		}
	}
}

func (s *Stream) watchAdaptation(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	lastRestart := time.Now()

	const (
		// Downgrade должен происходить раньше, чтобы не накапливать freeze/burst.
		downCooldown = 8 * time.Second

		// Upgrade только после длительной стабильности.
		upCooldown = 120 * time.Second

		// NACK — главный сигнал потерь.
		highNackThreshold = uint64(80)

		// Более ранний downgrade по REMB.
		rembDropRatio = 0.90

		// Для upgrade требуем запас REMB относительно maxrate.
		upgradeREMBHeadroom = 1.35
	)

	stableTicks := 0

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			s.mu.Lock()
			geom := s.geom
			current := s.profile
			s.mu.Unlock()

			remb := s.rembBps.Load()
			pli := s.pliCnt.Swap(0)
			nack := s.nackCnt.Swap(0)

			currentBps := uint64(current.BitrateKbps * 1000)

			hasLoss := nack >= highNackThreshold
			hasLowREMB := remb > 0 && remb < uint64(float64(currentBps)*rembDropRatio)

			if !hasLoss && !hasLowREMB {
				stableTicks++

				logger.RDAgent.Debugf(
					"Network stable: profile=%+v remb=%d pli=%d nack=%d stable_ticks=%d",
					current,
					remb,
					pli,
					nack,
					stableTicks,
				)

				// 24 тика по 5 секунд = 120 секунд устойчивой стабильности.
				if stableTicks < 24 {
					continue
				}

				if time.Since(lastRestart) < upCooldown {
					continue
				}

				target := nextHigherProfile(current)
				if target == current {
					continue
				}

				// Upgrade разрешаем только если REMB даёт запас над maxrate нового профиля.
				if remb > 0 {
					required := uint64(float64(target.MaxrateKbps*1000) * upgradeREMBHeadroom)
					if remb < required {
						logger.RDAgent.Debugf(
							"Upgrade skipped: insufficient REMB headroom current=%+v target=%+v remb=%d required=%d",
							current,
							target,
							remb,
							required,
						)
						continue
					}
				}

				lastRestart = time.Now()
				stableTicks = 0

				logger.RDAgent.Infof(
					"Network stable, upgrade bitrate: current=%+v target=%+v remb=%d pli=%d nack=%d",
					current,
					target,
					remb,
					pli,
					nack,
				)

				s.restart("network stable, bitrate upgrade", geom, target)
				continue
			}

			stableTicks = 0

			if time.Since(lastRestart) < downCooldown {
				logger.RDAgent.Debugf(
					"Network issue detected but downgrade skipped by cooldown: profile=%+v remb=%d pli=%d nack=%d",
					current,
					remb,
					pli,
					nack,
				)
				continue
			}

			target := nextLowerProfile(current)

			// Если REMB уже ниже low maxrate — сразу падаем на low.
			// Используем функцию профиля, чтобы не потерять CRF/static-thresh.
			if remb > 0 && remb < uint64(lowProfile24().MaxrateKbps*1000) {
				target = lowProfile24()
			}

			if target == current {
				continue
			}

			lastRestart = time.Now()

			logger.RDAgent.Infof(
				"Network issue, downgrade bitrate: current=%+v target=%+v remb=%d pli=%d nack=%d",
				current,
				target,
				remb,
				pli,
				nack,
			)

			s.restart("network issue, bitrate downgrade", geom, target)
		}
	}
}

func (s *Stream) requestKeyframeRecovery(reason string) {
	if !s.cfg.ForceKeyframeOnPLI {
		return
	}

	select {
	case s.keyframeReqCh <- reason:
	default:
		// Coalesce PLI/FIR bursts. Одного pending refresh достаточно.
	}
}

func (s *Stream) watchKeyframeRequests(ctx context.Context) {
	cooldown := time.Duration(s.cfg.PLIKeyframeCooldownMs) * time.Millisecond
	if cooldown <= 0 {
		cooldown = 750 * time.Millisecond
	}

	for {
		select {
		case <-ctx.Done():
			return

		case reason := <-s.keyframeReqCh:
			s.mu.Lock()
			since := time.Since(s.lastKeyframeForce)
			if !s.lastKeyframeForce.IsZero() && since < cooldown {
				s.mu.Unlock()

				logger.RDAgent.Debugf(
					"PLI/FIR keyframe recovery skipped by cooldown: reason=%s since=%s cooldown=%s",
					reason,
					since,
					cooldown,
				)
				continue
			}
			s.lastKeyframeForce = time.Now()
			s.mu.Unlock()

			s.forceKeyframeOnPLI(reason)
		}
	}
}

func (s *Stream) forceKeyframeOnPLI(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ctx == nil {
		return
	}

	geom := s.geom
	profile := s.profile

	switch s.cfg.VideoEncoder {
	case "libvpx":
		s.refreshEncoderLocked(
			"PLI/FIR recovery: refreshing VP8 encoder without profile downgrade",
			reason,
			geom,
			profile,
		)

	case "h264_mf":
		s.refreshEncoderLocked(
			"PLI/FIR recovery: refreshing H264 encoder without profile downgrade",
			reason,
			geom,
			profile,
		)

	case "av1_mf":
		s.refreshEncoderLocked(
			"PLI/FIR recovery: refreshing AV1 encoder without profile downgrade",
			reason,
			geom,
			profile,
		)

	default:
		logger.RDAgent.Warnf("PLI/FIR recovery ignored: unsupported encoder=%s", s.cfg.VideoEncoder)
	}
}

func (s *Stream) refreshEncoderLocked(logPrefix string, reason string, geom Geometry, profile Profile) {
	logger.RDAgent.Infof(
		"%s: reason=%s profile=%+v",
		logPrefix,
		reason,
		profile,
	)

	s.stopFFmpegLocked()
	s.geom = geom
	s.profile = profile
	s.lastFrameAt = time.Now()

	if err := s.startFFmpegLocked(); err != nil {
		logger.RDAgent.Errorf("%s failed: %v", logPrefix, err)
		return
	}

	screen.SetCaptureGeometry(screen.Geometry{
		X:      s.geom.X,
		Y:      s.geom.Y,
		Width:  s.geom.Width,
		Height: s.geom.Height,
	})
}

func (s *Stream) readRTCP(ctx context.Context) {
	buf := make([]byte, 1500)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, _, err := s.sender.Read(buf)
		if err != nil {
			return
		}

		pkts, err := rtcp.Unmarshal(buf[:n])
		if err != nil {
			continue
		}

		for _, pkt := range pkts {
			switch p := pkt.(type) {
			case *rtcp.PictureLossIndication:
				s.pliCnt.Add(1)
				s.requestKeyframeRecovery("rtcp pli")

			case *rtcp.FullIntraRequest:
				s.pliCnt.Add(1)
				s.requestKeyframeRecovery("rtcp fir")

			case *rtcp.TransportLayerNack:
				s.nackCnt.Add(uint64(len(p.Nacks)))

			case *rtcp.ReceiverEstimatedMaximumBitrate:
				s.rembBps.Store(uint64(p.Bitrate))
			}
		}
	}
}

func lowProfile24() Profile {
	return Profile{
		FPS:          16,
		BitrateKbps:  700,
		MaxrateKbps:  900,
		BufsizeKbps:  900,
		CRF:          38,
		StaticThresh: 900,
	}
}

func mediumProfile24() Profile {
	return Profile{
		FPS:          20,
		BitrateKbps:  1100,
		MaxrateKbps:  1600,
		BufsizeKbps:  344,
		CRF:          36,
		StaticThresh: 700,
	}
}

func highProfile24() Profile {
	return Profile{
		FPS:          24,
		BitrateKbps:  3800,
		MaxrateKbps:  6000,
		BufsizeKbps:  9000,
		CRF:          30,
		StaticThresh: 350,
	}
}

func ultraProfile24() Profile {
	return Profile{
		FPS:          24,
		BitrateKbps:  6000,
		MaxrateKbps:  9000,
		BufsizeKbps:  13500,
		CRF:          28,
		StaticThresh: 250,
	}
}

func nextLowerProfile(p Profile) Profile {
	switch {
	case p.BitrateKbps > 4500:
		return highProfile24()
	case p.BitrateKbps > 2800:
		return mediumProfile24()
	case p.BitrateKbps > 1600:
		return lowProfile24()
	default:
		return p
	}
}

func nextHigherProfile(p Profile) Profile {
	switch {
	case p.BitrateKbps < 2800:
		return mediumProfile24()
	case p.BitrateKbps < 4500:
		return highProfile24()
	case p.BitrateKbps < 7000:
		return ultraProfile24()
	default:
		return p
	}
}
