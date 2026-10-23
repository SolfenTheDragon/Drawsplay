package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"drawsplay/host/internal/wifilan"
)

// The diagnostic accepts only an explicit local synthetic fixture, never a
// desktop capture source. Its bounded file format is not the network protocol.
type syntheticVideo struct {
	input  io.Reader
	config []byte
	frames uint32
	fps    uint16
	read   uint32
	header [4]byte
}

func readSyntheticVideo(input io.Reader) (*syntheticVideo, error) {
	var header [16]byte
	if _, err := io.ReadFull(input, header[:]); err != nil {
		return nil, err
	}
	frames := binary.BigEndian.Uint32(header[4:])
	size := binary.BigEndian.Uint32(header[8:])
	if string(header[:4]) != "DSVT" || frames == 0 || frames > 1800 ||
		size < 16 || size > 65536 || binary.BigEndian.Uint32(header[12:]) != 0 {
		return nil, errors.New("invalid bounded synthetic-video fixture header")
	}
	video := &syntheticVideo{input: input, frames: frames, config: make([]byte, size)}
	if _, err := io.ReadFull(input, video.config); err != nil {
		return nil, err
	}
	video.fps = binary.BigEndian.Uint16(video.config[6:])
	if binary.BigEndian.Uint16(video.config) != 1 || video.fps == 0 || video.fps > 30 ||
		binary.BigEndian.Uint16(video.config[2:]) < 320 || binary.BigEndian.Uint16(video.config[2:]) > 1280 ||
		binary.BigEndian.Uint16(video.config[4:]) < 320 || binary.BigEndian.Uint16(video.config[4:]) > 720 {
		return nil, errors.New("synthetic video must be baseline config schema 1, 320..1280 by 320..720, at most 30 FPS")
	}
	return video, nil
}

func (s *syntheticVideo) readAU(storage []byte) (int, error) {
	if s.read >= s.frames {
		return 0, errors.New("synthetic AU count exceeded")
	}
	if _, err := io.ReadFull(s.input, s.header[:]); err != nil {
		return 0, err
	}
	size := binary.BigEndian.Uint32(s.header[:])
	if size == 0 || uint64(size) > uint64(len(storage)) {
		return 0, errors.New("synthetic AU exceeds negotiated buffer")
	}
	if _, err := io.ReadFull(s.input, storage[:size]); err != nil {
		return 0, err
	}
	s.read++
	return int(size), nil
}

func (s *syntheticVideo) finish() error {
	if s.read != s.frames {
		return errors.New("synthetic video ended before all access units")
	}
	if n, err := io.ReadFull(s.input, s.header[:1]); n != 0 || err != io.EOF {
		return errors.New("synthetic fixture has trailing bytes or read failure")
	}
	return nil
}

func streamSyntheticVideo(ctx context.Context, path string, control, video *tls.Conn, binding *wifilan.SessionBinding, limits wifilan.SessionHello, out io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("synthetic fixture must be a regular local file")
	}
	source, err := readSyntheticVideo(file)
	if err != nil {
		return err
	}
	if source.fps > limits.MaxFPS || binary.BigEndian.Uint16(source.config[2:]) > limits.MaxWidth ||
		binary.BigEndian.Uint16(source.config[4:]) > limits.MaxHeight {
		return wifilan.ErrSessionHello
	}
	if err := video.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	epoch := binding.Offer().Epoch
	writer := wifilan.NewFrameWriter(video)
	if err := writer.WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameConfig, Epoch: epoch, Sequence: 2, PayloadLength: uint32(len(source.config))}, source.config); err != nil {
		return err
	}
	if err := control.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	var message [24]byte
	controlReader := wifilan.NewFrameReader(control)
	header, payload, err := controlReader.ReadFrame(message[:])
	if err != nil {
		return fmt.Errorf("tablet did not configure its synthetic decoder: %w", err)
	}
	if header.Type != wifilan.FrameControl || header.Epoch != epoch || header.Sequence != 2 || header.CaptureMonoUS != 0 ||
		len(payload) != 4 || payload[0] != 7 || payload[1] != 0 || payload[2] != 0 || payload[3] != 1 {
		return errors.New("invalid synthetic decoder-ready message")
	}
	storage := make([]byte, limits.MaxAccessUnit)
	period := time.Second / time.Duration(source.fps)
	start := time.Now()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for index := uint32(0); index < source.frames; index++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		size, err := source.readAU(storage)
		if err != nil {
			return err
		}
		if err := video.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		if err := writer.WriteFrame(wifilan.FrameHeader{
			Type: wifilan.FrameVideoAU, Epoch: epoch, Sequence: index + 3, PayloadLength: uint32(size),
			CaptureMonoUS: uint64(time.Since(start).Microseconds()), DurationUS: 1000000 / uint32(source.fps),
		}, storage[:size]); err != nil {
			return err
		}
		// Do not burst old frames to catch up after a stalled write.
		delay := time.Until(start.Add(time.Duration(index+1) * period))
		if delay <= -period {
			return errors.New("synthetic sender stalled beyond one frame period; timing trial invalid")
		}
		timer.Reset(max(delay, 0))
	}
	if err := source.finish(); err != nil {
		return err
	}
	end := [4]byte{5, 0, 0, 1}
	if err := writer.WriteFrame(wifilan.FrameHeader{Type: wifilan.FrameControl, Epoch: epoch, Sequence: source.frames + 3, PayloadLength: 4}, end[:]); err != nil {
		return err
	}
	if err := control.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	header, payload, err = controlReader.ReadFrame(message[:])
	if err != nil {
		return fmt.Errorf("tablet did not report synthetic decode completion: %w", err)
	}
	if header.Type != wifilan.FrameControl || header.Epoch != epoch || header.Sequence != 3 || header.CaptureMonoUS != 0 ||
		len(payload) != 24 || payload[0] != 6 || payload[1] != 0 || binary.BigEndian.Uint16(payload[2:]) != 1 {
		return errors.New("invalid synthetic decode result")
	}
	received := binary.BigEndian.Uint32(payload[4:])
	decoded := binary.BigEndian.Uint32(payload[8:])
	renderReleased := binary.BigEndian.Uint32(payload[12:])
	queueDrops := binary.BigEndian.Uint32(payload[16:])
	decodeDrops := binary.BigEndian.Uint32(payload[20:])
	if received != source.frames || decoded > received || renderReleased > decoded ||
		uint64(renderReleased)*100 < uint64(received)*90 {
		return fmt.Errorf("synthetic decode acceptance failed: sent=%d received=%d decoded=%d renderReleased=%d queueDrops=%d decodeDrops=%d (requires >=90%% render releases)", source.frames, received, decoded, renderReleased, queueDrops, decodeDrops)
	}
	_, err = fmt.Fprintf(out, "Synthetic Wi-Fi video decoded: received=%d decoded=%d renderReleased=%d queueDrops=%d decodeDrops=%d\n", received, decoded, renderReleased, queueDrops, decodeDrops)
	return err
}
