package desktop

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

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
}

type Stream struct {
	sessionID string
	track     *webrtc.TrackLocalStaticSample
	sender    *webrtc.RTPSender

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
}

func NewStream(sessionID string, track *webrtc.TrackLocalStaticSample, sender *webrtc.RTPSender) (*Stream, error) {
	return &Stream{
		sessionID: sessionID,
		track:     track,
		sender:    sender,
		profile:   ultraProfile24(),
	}, nil
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

	if err := s.startFFmpegLocked(); err != nil {
		cancel()
		s.ctx = nil
		s.cancel = nil
		return err
	}

	go s.readRTCP(ctx)
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
	logger.RDAgent.Info("Desktop stream stopped")
}

func currentGeometry() (Geometry, error) {
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
		return Geometry{}, fmt.Errorf("invalid primary monitor geometry: %+v", geom)
	}

	return geom, nil
}

func (s *Stream) ffmpegArgs() []string {
	g := s.geom
	p := s.profile

	return []string{
		"-hide_banner",
		"-loglevel", "warning",

		"-f", "gdigrab",
		"-draw_mouse", "1",
		"-framerate", strconv.Itoa(p.FPS),
		"-offset_x", strconv.Itoa(g.X),
		"-offset_y", strconv.Itoa(g.Y),
		"-video_size", fmt.Sprintf("%dx%d", g.Width, g.Height),
		"-i", "desktop",

		"-an",

		"-c:v", "libvpx",
		"-deadline", "realtime",
		"-cpu-used", "8",
		"-lag-in-frames", "0",
		"-error-resilient", "1",
		"-auto-alt-ref", "0",
		"-quality", "realtime",

		"-g", strconv.Itoa(p.FPS * 2),
		"-b:v", fmt.Sprintf("%dk", p.BitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", p.MaxrateKbps),
		"-bufsize", fmt.Sprintf("%dk", p.BufsizeKbps),

		"-f", "ivf",
		"pipe:1",
	}
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

	go func(profile Profile) {
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
					logger.RDAgent.Errorf("read VP8 frame stopped: %v", err)
				}
				return
			}

			s.mu.Lock()
			s.lastFrameAt = time.Now()
			s.mu.Unlock()

			frameCount++
			if time.Since(lastLog) >= 2*time.Second {
				logger.RDAgent.Debugf("VP8 frames sent: %d in last 2s", frameCount)
				frameCount = 0
				lastLog = time.Now()
			}

			if err := s.track.WriteSample(media.Sample{
				Data:     frame,
				Duration: frameDuration,
			}); err != nil && err != io.ErrClosedPipe {
				logger.RDAgent.Errorf("write video sample failed: %v", err)
				return
			}
		}
	}(s.profile)

	logger.RDAgent.Infof("FFmpeg started: %s %s", bin, strings.Join(args, " "))
	return nil
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
	}
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

			if geom != old {
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
		downCooldown = 15 * time.Second
		upCooldown   = 60 * time.Second

		// Если за 5 секунд прилетело столько NACK/PLI —
		// считаем, что канал реально деградировал.
		highNackThreshold = uint64(120)
		highPLIThreshold  = uint64(4)

		// Если receiver estimated bitrate ниже текущего bitrate
		// хотя бы на 25%, тоже считаем это сетевой деградацией.
		rembDropRatio = 0.75
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

			hasLoss := nack >= highNackThreshold || pli >= highPLIThreshold
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

				// Апгрейдим только после долгой стабильности.
				if stableTicks < 12 {
					continue
				}

				if time.Since(lastRestart) < upCooldown {
					continue
				}

				target := nextHigherProfile(current)
				if target == current {
					continue
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

			// Если REMB очень низкий — сразу падаем сильнее.
			if remb > 0 && remb < 2_000_000 {
				target = Profile{
					FPS:         24,
					BitrateKbps: 1600,
					MaxrateKbps: 2200,
					BufsizeKbps: 3300,
				}
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
			case *rtcp.FullIntraRequest:
				s.pliCnt.Add(1)
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
		FPS:         24,
		BitrateKbps: 1600,
		MaxrateKbps: 2200,
		BufsizeKbps: 3300,
	}
}

func mediumProfile24() Profile {
	return Profile{
		FPS:         24,
		BitrateKbps: 2800,
		MaxrateKbps: 3800,
		BufsizeKbps: 5700,
	}
}

func highProfile24() Profile {
	return Profile{
		FPS:         24,
		BitrateKbps: 4500,
		MaxrateKbps: 6000,
		BufsizeKbps: 9000,
	}
}

func ultraProfile24() Profile {
	return Profile{
		FPS:         24,
		BitrateKbps: 7000,
		MaxrateKbps: 9000,
		BufsizeKbps: 13500,
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
