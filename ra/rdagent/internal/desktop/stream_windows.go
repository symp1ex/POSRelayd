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

	videoWriteSlowThreshold        = 120 * time.Millisecond
	keyframeRecoveryCooldownFloor  = 3 * time.Second
	keyframeRecoveryCongestionHold = 2 * time.Second
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

	CRF          int
	StaticThresh int
}

type keyframeRequest struct {
	reason     string
	receivedAt time.Time
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

	videoFramesSent         atomic.Uint64
	videoBytesSent          atomic.Uint64
	videoKeyframesSent      atomic.Uint64
	videoSlowWrites         atomic.Uint64
	videoMaxWriteNanos      atomic.Int64
	videoLastSlowWriteNanos atomic.Int64

	keyframeReqCh     chan keyframeRequest
	lastKeyframeForce time.Time

	pendingKeyframeSeq    uint64
	pendingKeyframePLIAt  time.Time
	pendingKeyframeSentAt time.Time
	pendingKeyframeReason string
}

func NewStream(sessionID string, track *webrtc.TrackLocalStaticSample, sender *webrtc.RTPSender, cfg config.Config) (*Stream, error) {
	return &Stream{
		sessionID: sessionID,
		track:     track,
		sender:    sender,
		cfg:       cfg,
		profile:   mediumProfile24(),

		keyframeReqCh: make(chan keyframeRequest, 1),
	}, nil
}

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
		return highProfile24()
	}
}

func fixedProfileForQuality(quality string) (Profile, bool) {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "low":
		return lowProfile24(), true
	case "medium":
		return mediumProfile24(), true
	case "high":
		return highProfile24(), true
	case "ultra":
		return ultraProfile24(), true
	default:
		return Profile{}, false
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

	if fixed, ok := fixedProfileForQuality(s.cfg.VideoQuality); ok {
		s.profile = fixed
		logger.RDAgent.Infof("Using fixed video quality profile: quality=%s profile=%+v", s.cfg.VideoQuality, s.profile)
	} else {
		s.profile = initialProfileForGeometry(geom)
	}

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
	go s.watchLatencyStats(ctx)

	if _, fixed := fixedProfileForQuality(s.cfg.VideoQuality); !fixed {
		go s.watchAdaptation(ctx)
	} else {
		logger.RDAgent.Infof("Network adaptation disabled for fixed video quality: %s", s.cfg.VideoQuality)
	}

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
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd != 0 {
		monitor, _, _ := procMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
		if geom, ok := monitorGeometry(monitor); ok {
			return geom, true
		}
	}
	var pt winPoint
	if ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); ret != 0 {
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

		"-flags", "+low_delay",
		"-bf", "0",
		"-g", strconv.Itoa(p.FPS),
		"-b:v", fmt.Sprintf("%dk", p.BitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", p.MaxrateKbps),
		"-bufsize", fmt.Sprintf("%dk", p.BufsizeKbps),

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

		"-crf", strconv.Itoa(p.CRF),
		"-b:v", fmt.Sprintf("%dk", p.BitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", p.MaxrateKbps),
		"-bufsize", fmt.Sprintf("%dk", p.BufsizeKbps),

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
		"-vf", "hwdownload,format=bgra,format=nv12",
		"-pix_fmt", "nv12",

		"-c:v", "av1_mf",

		"-hw_encoding", boolToFFmpegInt(s.cfg.MFHWEncoding),

		"-flags", "+low_delay",
		"-bf", "0",
		"-g", strconv.Itoa(p.FPS*2),
		"-b:v", fmt.Sprintf("%dk", p.BitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", p.MaxrateKbps),
		"-bufsize", fmt.Sprintf("%dk", p.BufsizeKbps),

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
		local := filepath.Join(wd, "ffmpeg", "ffmpeg.exe")
		if _, statErr := os.Stat(local); statErr == nil {
			return local
		}
	}

	return "ffmpeg"
}

type ffmpegProcess struct {
	mu       sync.Mutex
	hProcess windows.Handle
	hThread  windows.Handle
	pid      uint32
	stdin    *os.File
	stdout   *os.File
	stderr   *os.File
}

func (p *ffmpegProcess) PID() uint32 {
	if p == nil {
		return 0
	}
	return p.pid
}

func (p *ffmpegProcess) WriteControlLine(line string) error {
	if p == nil {
		return fmt.Errorf("ffmpeg process is nil")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stdin == nil {
		return fmt.Errorf("ffmpeg stdin is not available")
	}

	if _, err := p.stdin.WriteString(line + "\n"); err != nil {
		return err
	}
	return nil
}

func (p *ffmpegProcess) KillAndWait() {
	if p == nil {
		return
	}

	p.mu.Lock()
	stdin := p.stdin
	p.stdin = nil
	p.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
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

	stdinR, stdinW, err := makeChildStdinPipe()
	if err != nil {
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stdoutW)
		_ = windows.CloseHandle(stderrR)
		_ = windows.CloseHandle(stderrW)
		return nil, nil, nil, fmt.Errorf("stdin pipe: %w", err)
	}

	cmdline := commandLine(bin, args)

	si := new(windows.StartupInfo)
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	si.ShowWindow = windows.SW_HIDE
	si.StdOutput = stdoutW
	si.StdErr = stderrW
	si.StdInput = stdinR
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
	_ = windows.CloseHandle(stdinR)

	if err != nil {
		_ = windows.CloseHandle(stdoutR)
		_ = windows.CloseHandle(stderrR)
		_ = windows.CloseHandle(stdinW)
		return nil, nil, nil, fmt.Errorf("CreateProcess desktop=%s failed: %w", desktopFullName, err)
	}

	stdinFile := os.NewFile(uintptr(stdinW), "ffmpeg-stdin")
	stdoutFile := os.NewFile(uintptr(stdoutR), "ffmpeg-stdout")
	stderrFile := os.NewFile(uintptr(stderrR), "ffmpeg-stderr")

	proc := &ffmpegProcess{
		hProcess: pi.Process,
		hThread:  pi.Thread,
		pid:      pi.ProcessId,
		stdin:    stdinFile,
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

func makeChildStdinPipe() (windows.Handle, windows.Handle, error) {
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1

	var read windows.Handle
	var write windows.Handle

	if err := windows.CreatePipe(&read, &write, &sa, 0); err != nil {
		return 0, 0, err
	}

	if err := windows.SetHandleInformation(write, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
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
		"FFmpeg process started on desktop: name=%s full=%s pid=%d",
		desktop.Name(),
		desktopFullName,
		proc.PID(),
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

		keyframe := isIVFKeyframe(s.cfg.VideoCodec, frame)

		frameCount++
		if time.Since(lastLog) >= 2*time.Second {
			logger.RDAgent.Debugf("%s IVF frames sent: %d in last 2s", s.cfg.VideoCodec, frameCount)
			frameCount = 0
			lastLog = time.Now()
		}

		if err := s.writeVideoSample(frame, frameDuration, s.cfg.VideoCodec, keyframe); err != nil {
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
			logger.RDAgent.Errorf("read H264 access unit stopped: %v", err)
			return
		}

		if len(accessUnit) == 0 {
			continue
		}

		keyframe := h264AnnexBHasIDR(accessUnit)

		frameCount++
		if time.Since(lastLog) >= 2*time.Second {
			logger.RDAgent.Debugf("H264 access units sent: %d in last 2s", frameCount)
			frameCount = 0
			lastLog = time.Now()
		}

		if err := s.writeVideoSample(accessUnit, frameDuration, "h264", keyframe); err != nil {
			logger.RDAgent.Errorf("write H264 video sample failed: %v", err)
			return
		}
	}
}

func (s *Stream) writeVideoSample(data []byte, duration time.Duration, codec string, keyframe bool) error {
	s.markFrameSent()
	if keyframe {
		s.videoKeyframesSent.Add(1)
		s.observeOutgoingKeyframe(codec, len(data))
	}

	start := time.Now()
	err := s.track.WriteSample(media.Sample{
		Data:     data,
		Duration: duration,
	})
	elapsed := time.Since(start)

	s.recordVideoWrite(len(data), elapsed)

	if err != nil && err != io.ErrClosedPipe {
		return err
	}
	return nil
}

func (s *Stream) recordVideoWrite(size int, elapsed time.Duration) {
	s.videoFramesSent.Add(1)
	s.videoBytesSent.Add(uint64(size))

	nanos := elapsed.Nanoseconds()
	for {
		current := s.videoMaxWriteNanos.Load()
		if nanos <= current || s.videoMaxWriteNanos.CompareAndSwap(current, nanos) {
			break
		}
	}

	if elapsed >= videoWriteSlowThreshold {
		s.videoSlowWrites.Add(1)
		s.videoLastSlowWriteNanos.Store(time.Now().UnixNano())
	}
}

func (s *Stream) videoBackpressureActive() bool {
	last := s.videoLastSlowWriteNanos.Load()
	if last == 0 {
		return false
	}
	return time.Since(time.Unix(0, last)) < keyframeRecoveryCongestionHold
}

func (s *Stream) markFrameSent() {
	s.mu.Lock()
	s.lastFrameAt = time.Now()
	s.mu.Unlock()
}

func (s *Stream) observeOutgoingKeyframe(codec string, size int) {
	now := time.Now()

	s.mu.Lock()
	seq := s.pendingKeyframeSeq
	pliAt := s.pendingKeyframePLIAt
	sentAt := s.pendingKeyframeSentAt
	reason := s.pendingKeyframeReason
	pending := !sentAt.IsZero()
	if pending {
		s.pendingKeyframePLIAt = time.Time{}
		s.pendingKeyframeSentAt = time.Time{}
		s.pendingKeyframeReason = ""
	}
	s.mu.Unlock()

	if pending {
		logger.RDAgent.Infof(
			"Forced keyframe observed: seq=%d reason=%s codec=%s size=%d pli_to_keyframe=%s command_to_keyframe=%s",
			seq,
			reason,
			codec,
			size,
			now.Sub(pliAt),
			now.Sub(sentAt),
		)
		return
	}

	logger.RDAgent.Debugf("Outgoing keyframe observed: codec=%s size=%d", codec, size)
}

func isIVFKeyframe(codec string, frame []byte) bool {
	switch codec {
	case "vp8":
		return len(frame) > 0 && frame[0]&0x01 == 0
	case "av1":
		return isAV1Keyframe(frame)
	default:
		return false
	}
}

func h264AnnexBHasIDR(accessUnit []byte) bool {
	for i := 0; i+4 < len(accessUnit); i++ {
		if accessUnit[i] != 0 || accessUnit[i+1] != 0 {
			continue
		}

		startCodeLen := 0
		if accessUnit[i+2] == 1 {
			startCodeLen = 3
		} else if i+3 < len(accessUnit) && accessUnit[i+2] == 0 && accessUnit[i+3] == 1 {
			startCodeLen = 4
		}

		if startCodeLen == 0 {
			continue
		}

		nalStart := i + startCodeLen
		if nalStart < len(accessUnit) && accessUnit[nalStart]&0x1F == 5 {
			return true
		}
	}

	return false
}

func isAV1Keyframe(frame []byte) bool {
	for offset := 0; offset < len(frame); {
		header := frame[offset]
		offset++

		obuType := (header >> 3) & 0x0F
		hasExtension := header&0x04 != 0
		hasSize := header&0x02 != 0

		if hasExtension {
			if offset >= len(frame) {
				return false
			}
			offset++
		}

		if !hasSize {
			return false
		}

		payloadSize, n := readLEB128(frame[offset:])
		if n <= 0 {
			return false
		}
		offset += n

		if payloadSize < 0 || offset+payloadSize > len(frame) {
			return false
		}

		payload := frame[offset : offset+payloadSize]
		offset += payloadSize

		if obuType == 3 || obuType == 6 {
			return av1FrameHeaderIsKeyframe(payload)
		}
	}

	return false
}

func readLEB128(data []byte) (int, int) {
	value := 0
	for i := 0; i < len(data) && i < 8; i++ {
		value |= int(data[i]&0x7F) << (i * 7)
		if data[i]&0x80 == 0 {
			return value, i + 1
		}
	}
	return -1, 0
}

func av1FrameHeaderIsKeyframe(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}

	br := bitReader{data: payload}

	showExistingFrame, ok := br.readBit()
	if !ok || showExistingFrame == 1 {
		return false
	}

	frameType, ok := br.readBits(2)
	return ok && frameType == 0
}

type bitReader struct {
	data []byte
	bit  int
}

func (r *bitReader) readBit() (uint, bool) {
	if r.bit >= len(r.data)*8 {
		return 0, false
	}

	v := (r.data[r.bit/8] >> uint(7-r.bit%8)) & 1
	r.bit++
	return uint(v), true
}

func (r *bitReader) readBits(n int) (uint, bool) {
	var out uint
	for i := 0; i < n; i++ {
		bit, ok := r.readBit()
		if !ok {
			return 0, false
		}
		out = (out << 1) | bit
	}
	return out, true
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

func (s *Stream) watchLatencyStats(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			frames := s.videoFramesSent.Swap(0)
			bytes := s.videoBytesSent.Swap(0)
			keyframes := s.videoKeyframesSent.Swap(0)
			slowWrites := s.videoSlowWrites.Swap(0)
			maxWrite := time.Duration(s.videoMaxWriteNanos.Swap(0))

			if frames == 0 {
				continue
			}

			if slowWrites > 0 {
				logger.RDAgent.Warnf(
					"Desktop video latency pressure: frames=%d bytes=%d keyframes=%d slow_writes=%d max_write=%s profile=%+v",
					frames,
					bytes,
					keyframes,
					slowWrites,
					maxWrite,
					s.currentProfileSnapshot(),
				)
				continue
			}

			logger.RDAgent.Debugf(
				"Desktop video latency stats: frames=%d bytes=%d keyframes=%d max_write=%s profile=%+v",
				frames,
				bytes,
				keyframes,
				maxWrite,
				s.currentProfileSnapshot(),
			)
		}
	}
}

func (s *Stream) currentProfileSnapshot() Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile
}

func (s *Stream) watchAdaptation(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	lastRestart := time.Now()

	const (
		downCooldown = 4 * time.Second

		upCooldown = 120 * time.Second

		highNackThreshold = uint64(30)

		rembDropRatio = 0.90

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
			hasVideoPressure := s.videoBackpressureActive()

			if !hasLoss && !hasLowREMB && !hasVideoPressure {
				stableTicks++

				logger.RDAgent.Debugf(
					"Network stable: profile=%+v remb=%d pli=%d nack=%d stable_ticks=%d",
					current,
					remb,
					pli,
					nack,
					stableTicks,
				)

				if stableTicks < 60 {
					continue
				}

				if time.Since(lastRestart) < upCooldown {
					continue
				}

				target := nextHigherProfile(current)
				if target == current {
					continue
				}

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
					"Network or latency issue detected but downgrade skipped by cooldown: profile=%+v remb=%d pli=%d nack=%d video_pressure=%t",
					current,
					remb,
					pli,
					nack,
					hasVideoPressure,
				)
				continue
			}

			target := nextLowerProfile(current)

			if remb > 0 && remb < uint64(lowProfile24().MaxrateKbps*1000) {
				target = lowProfile24()
			}

			if target == current {
				continue
			}

			lastRestart = time.Now()

			logger.RDAgent.Infof(
				"Network or latency issue, downgrade bitrate: current=%+v target=%+v remb=%d pli=%d nack=%d video_pressure=%t",
				current,
				target,
				remb,
				pli,
				nack,
				hasVideoPressure,
			)

			s.restart("network issue, bitrate downgrade", geom, target)
		}
	}
}

func (s *Stream) requestKeyframeRecovery(reason string) {
	if !s.cfg.ForceKeyframeOnPLI {
		return
	}

	req := keyframeRequest{
		reason:     reason,
		receivedAt: time.Now(),
	}

	select {
	case s.keyframeReqCh <- req:
	default:
	}
}

func (s *Stream) watchKeyframeRequests(ctx context.Context) {
	cooldown := time.Duration(s.cfg.PLIKeyframeCooldownMs) * time.Millisecond
	if cooldown < keyframeRecoveryCooldownFloor {
		logger.RDAgent.Infof(
			"PLI/FIR keyframe cooldown raised for latency safety: configured=%s effective=%s",
			cooldown,
			keyframeRecoveryCooldownFloor,
		)
		cooldown = keyframeRecoveryCooldownFloor
	}

	for {
		select {
		case <-ctx.Done():
			return

		case req := <-s.keyframeReqCh:
			if s.videoBackpressureActive() {
				logger.RDAgent.Warnf(
					"PLI/FIR keyframe recovery skipped during video latency pressure: reason=%s",
					req.reason,
				)
				continue
			}

			s.mu.Lock()
			since := time.Since(s.lastKeyframeForce)
			if !s.lastKeyframeForce.IsZero() && since < cooldown {
				s.mu.Unlock()

				logger.RDAgent.Debugf(
					"PLI/FIR keyframe recovery skipped by cooldown: reason=%s since=%s cooldown=%s",
					req.reason,
					since,
					cooldown,
				)
				continue
			}
			s.lastKeyframeForce = time.Now()
			s.mu.Unlock()

			s.forceKeyframeOnPLI(req)
		}
	}
}

func (s *Stream) forceKeyframeOnPLI(req keyframeRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ctx == nil {
		return
	}

	if s.videoBackpressureActive() {
		logger.RDAgent.Warnf(
			"PLI/FIR keyframe command suppressed by active video latency pressure: reason=%s",
			req.reason,
		)
		return
	}

	switch s.cfg.VideoEncoder {
	case "libvpx", "h264_mf", "av1_mf":
		if err := s.requestFFmpegKeyframeLocked(req); err != nil {
			logger.RDAgent.Warnf(
				"PLI/FIR keyframe command failed, refreshing encoder as fallback: reason=%s error=%v",
				req.reason,
				err,
			)
			s.refreshEncoderLocked(
				"PLI/FIR recovery fallback: refreshing encoder without profile downgrade",
				req.reason,
				s.geom,
				s.profile,
			)
		}

	default:
		logger.RDAgent.Warnf("PLI/FIR recovery ignored: unsupported encoder=%s", s.cfg.VideoEncoder)
	}
}

func (s *Stream) requestFFmpegKeyframeLocked(req keyframeRequest) error {
	if s.ffmpeg == nil {
		return fmt.Errorf("ffmpeg is not running")
	}

	sentAt := time.Now()
	if err := s.ffmpeg.WriteControlLine("force_keyframe"); err != nil {
		return err
	}

	s.pendingKeyframeSeq++
	seq := s.pendingKeyframeSeq
	s.pendingKeyframePLIAt = req.receivedAt
	s.pendingKeyframeSentAt = sentAt
	s.pendingKeyframeReason = req.reason
	pid := s.ffmpeg.PID()
	codec := s.cfg.VideoCodec
	encoder := s.cfg.VideoEncoder
	profile := s.profile
	ctx := s.ctx

	logger.RDAgent.Infof(
		"PLI/FIR keyframe recovery command sent: seq=%d reason=%s pli_to_command=%s pid=%d encoder=%s codec=%s profile=%+v",
		seq,
		req.reason,
		sentAt.Sub(req.receivedAt),
		pid,
		encoder,
		codec,
		profile,
	)

	go s.watchForcedKeyframeObservation(ctx, seq, sentAt, req.reason, codec)
	return nil
}

func (s *Stream) watchForcedKeyframeObservation(ctx context.Context, seq uint64, sentAt time.Time, reason string, codec string) {
	timeout := 2500 * time.Millisecond
	if codec == "av1" {
		timeout = 3500 * time.Millisecond
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	s.mu.Lock()
	if s.ctx != ctx || s.pendingKeyframeSeq != seq || s.pendingKeyframeSentAt.IsZero() {
		s.mu.Unlock()
		return
	}

	if codec == "av1" {
		logger.RDAgent.Warnf(
			"Forced AV1 keyframe not observed before timeout: seq=%d reason=%s since_command=%s; keeping FFmpeg alive because AV1 keyframe detection is best-effort",
			seq,
			reason,
			time.Since(sentAt),
		)
		s.mu.Unlock()
		return
	}

	if s.videoBackpressureActive() {
		s.pendingKeyframeSentAt = time.Time{}
		s.pendingKeyframeReason = ""
		s.mu.Unlock()

		logger.RDAgent.Warnf(
			"Forced keyframe not observed before timeout, refresh skipped during video latency pressure: seq=%d reason=%s since_command=%s",
			seq,
			reason,
			time.Since(sentAt),
		)
		return
	}

	geom := s.geom
	profile := s.profile
	s.pendingKeyframeSentAt = time.Time{}
	s.pendingKeyframeReason = ""
	s.mu.Unlock()

	logger.RDAgent.Warnf(
		"Forced keyframe not observed before timeout, refreshing encoder as fallback: seq=%d reason=%s since_command=%s",
		seq,
		reason,
		time.Since(sentAt),
	)

	s.refreshEncoder("PLI/FIR recovery fallback: forced keyframe not observed", reason, geom, profile)
}

func (s *Stream) refreshEncoder(logPrefix string, reason string, geom Geometry, profile Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ctx == nil {
		return
	}

	s.refreshEncoderLocked(logPrefix, reason, geom, profile)
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
				logger.RDAgent.Info("RTCP PLI received")
				s.requestKeyframeRecovery("rtcp pli")

			case *rtcp.FullIntraRequest:
				s.pliCnt.Add(1)
				logger.RDAgent.Info("RTCP FIR received")
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
		BitrateKbps:  900,
		MaxrateKbps:  1100,
		BufsizeKbps:  650,
		CRF:          38,
		StaticThresh: 800,
	}
}

func mediumProfile24() Profile {
	return Profile{
		FPS:          20,
		BitrateKbps:  1500,
		MaxrateKbps:  2900,
		BufsizeKbps:  1200,
		CRF:          34,
		StaticThresh: 600,
	}
}

func highProfile24() Profile {
	return Profile{
		FPS:          24,
		BitrateKbps:  2800,
		MaxrateKbps:  5000,
		BufsizeKbps:  1800,
		CRF:          32,
		StaticThresh: 500,
	}
}

func ultraProfile24() Profile {
	return Profile{
		FPS:          24,
		BitrateKbps:  5000,
		MaxrateKbps:  8000,
		BufsizeKbps:  2600,
		CRF:          30,
		StaticThresh: 350,
	}
}

func nextLowerProfile(p Profile) Profile {
	switch {
	case p.BitrateKbps >= ultraProfile24().BitrateKbps:
		return highProfile24()

	case p.BitrateKbps >= highProfile24().BitrateKbps:
		return mediumProfile24()

	case p.BitrateKbps >= mediumProfile24().BitrateKbps:
		return lowProfile24()

	default:
		return p
	}
}

func nextHigherProfile(p Profile) Profile {
	switch {
	case p.BitrateKbps < mediumProfile24().BitrateKbps:
		return mediumProfile24()

	case p.BitrateKbps < highProfile24().BitrateKbps:
		return highProfile24()

	case p.BitrateKbps < ultraProfile24().BitrateKbps:
		return ultraProfile24()

	default:
		return p
	}
}
