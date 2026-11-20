package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"kadrroom/internal/catalogmedia"
)

const outputLimit = 512 << 20
const maximumDuration = 4 * 60 * 60

func (w worker) transcode(ctx context.Context, job mediaJob) (string, int, error) {
	output, err := os.CreateTemp(w.dir, ".transcode-")
	if err != nil {
		return "", 0, err
	}
	path := output.Name()
	if err := output.Close(); err != nil {
		_ = os.Remove(path)
		return "", 0, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(path)
		}
	}()
	input := filepath.Join(w.dir, job.id+catalogmedia.SourceSuffix)
	args := []string{
		"-nostdin", "-hide_banner", "-v", "error", "-nostats", "-progress", "pipe:1",
		"-max_alloc", strconv.Itoa(128 << 20), "-threads", "1", "-filter_threads", "1",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", "mov,mp4,m4a,3gp,3g2,mj2,mp3,wav,matroska,webm",
		"-i", input, "-map_metadata", "-1", "-map_chapters", "-1",
	}
	if job.mediaType == "video" {
		args = append(args,
			"-map", "0:V:0", "-map", "0:a:0?", "-c:v", "libx264", "-preset", "veryfast",
			"-vf", "scale='min(iw,1920)':'min(ih,1080)':force_original_aspect_ratio=decrease,scale=trunc(iw/2)*2:trunc(ih/2)*2",
			"-pix_fmt", "yuv420p", "-threads", "2")
	} else {
		args = append(args, "-map", "0:a:0", "-vn")
	}
	args = append(args, "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart",
		"-fs", strconv.Itoa(outputLimit), "-f", "mp4", "-y", path)

	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(commandCtx, w.ffmpeg, args...)
	command.Stderr = io.Discard
	progress, err := command.StdoutPipe()
	if err != nil {
		return "", 0, err
	}
	if err := command.Start(); err != nil {
		return "", 0, err
	}
	seconds, scanErr := readProgress(progress, cancel)
	waitErr := command.Wait()
	if scanErr != nil || waitErr != nil {
		return "", 0, fmt.Errorf("ffmpeg conversion failed: %w", errors.Join(scanErr, waitErr))
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	if seconds <= 0 || info.Size() <= 0 || info.Size() >= outputLimit-64*1024 {
		return "", 0, errors.New("converted media is empty or exceeds the output limit")
	}
	success = true
	return path, int(math.Ceil(seconds)), nil
}

func readProgress(progress io.Reader, cancel context.CancelFunc) (float64, error) {
	var seconds float64
	scanner := bufio.NewScanner(progress)
	for scanner.Scan() {
		value, ok := strings.CutPrefix(scanner.Text(), "out_time=")
		if !ok {
			continue
		}
		seconds = max(seconds, elapsedSeconds(value))
		if seconds > maximumDuration {
			cancel()
			return 0, errors.New("duration exceeds four hours")
		}
	}
	if err := scanner.Err(); err != nil {
		cancel()
		return 0, err
	}
	return seconds, nil
}

func elapsedSeconds(value string) float64 {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return 0
	}
	hours, hourErr := strconv.Atoi(parts[0])
	minutes, minErr := strconv.Atoi(parts[1])
	seconds, secErr := strconv.ParseFloat(parts[2], 64)
	if hourErr != nil || minErr != nil || secErr != nil || hours < 0 || minutes < 0 || minutes > 59 ||
		seconds < 0 || seconds >= 60 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0
	}
	return float64(hours)*3600 + float64(minutes)*60 + seconds
}
