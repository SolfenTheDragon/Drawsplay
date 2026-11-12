package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type deviceCalibration struct {
	ScaleX  float32 `json:"scaleX"`
	ScaleY  float32 `json:"scaleY"`
	OffsetX float32 `json:"offsetX"`
	OffsetY float32 `json:"offsetY"`
}

func defaultDeviceCalibration() deviceCalibration {
	return deviceCalibration{
		ScaleX: 1.0,
		ScaleY: 1.0,
	}
}

func (c deviceCalibration) normalized() deviceCalibration {
	out := c
	if out.ScaleX == 0 {
		out.ScaleX = 1
	}
	if out.ScaleY == 0 {
		out.ScaleY = 1
	}
	return out
}

func (c deviceCalibration) validate() error {
	c = c.normalized()
	if c.ScaleX < 0.5 || c.ScaleX > 1.5 {
		return errors.New("scaleX must be between 0.5 and 1.5")
	}
	if c.ScaleY < 0.5 || c.ScaleY > 1.5 {
		return errors.New("scaleY must be between 0.5 and 1.5")
	}
	if c.OffsetX < -0.25 || c.OffsetX > 0.25 {
		return errors.New("offsetX must be between -0.25 and 0.25")
	}
	if c.OffsetY < -0.25 || c.OffsetY > 0.25 {
		return errors.New("offsetY must be between -0.25 and 0.25")
	}
	return nil
}

func (c deviceCalibration) apply(x, y float32) (float32, float32) {
	c = c.normalized()
	x = float32(clamp01(float64(x*c.ScaleX + c.OffsetX)))
	y = float32(clamp01(float64(y*c.ScaleY + c.OffsetY)))
	return x, y
}

var calibrationState = struct {
	mu     sync.RWMutex
	loaded bool
	values map[string]deviceCalibration
}{
	values: make(map[string]deviceCalibration),
}

func calibrationPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "calibration.json"), nil
}

func ensureCalibrationLoaded() {
	calibrationState.mu.Lock()
	defer calibrationState.mu.Unlock()
	if calibrationState.loaded {
		return
	}
	calibrationState.loaded = true
	path, err := calibrationPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var parsed map[string]deviceCalibration
	if err := json.Unmarshal(data, &parsed); err != nil {
		return
	}
	for k, v := range parsed {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		calibrationState.values[key] = v.normalized()
	}
}

func saveCalibrationsLocked() error {
	path, err := calibrationPath()
	if err != nil {
		return err
	}
	return writeJSONAtomic(path, calibrationState.values)
}

func calibrationForDevice(device string) deviceCalibration {
	ensureCalibrationLoaded()
	key := strings.TrimSpace(device)
	if key == "" {
		return defaultDeviceCalibration()
	}
	calibrationState.mu.RLock()
	defer calibrationState.mu.RUnlock()
	if c, ok := calibrationState.values[key]; ok {
		return c.normalized()
	}
	return defaultDeviceCalibration()
}

func setCalibrationForDevice(device string, c deviceCalibration) error {
	device = strings.TrimSpace(device)
	if device == "" {
		return errors.New("device must not be empty")
	}
	if err := c.validate(); err != nil {
		return err
	}
	ensureCalibrationLoaded()
	calibrationState.mu.Lock()
	defer calibrationState.mu.Unlock()
	calibrationState.values[device] = c.normalized()
	return saveCalibrationsLocked()
}
