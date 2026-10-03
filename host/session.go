package main

import "sync"

// sessionOwnership keeps input and video on one transport. Video waiters keep
// ownership stable while an older connection tears down for a reconnect.
type sessionOwnership struct {
	mu                     sync.Mutex
	transport              string
	inputActive            bool
	videoActive            bool
	videoWaiters           int
	videoHandoffInProgress bool
	videoHandoff           chan struct{}
}

var hostSessionOwnership sessionOwnership

func (s *sessionOwnership) acquireInput(transport string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if transport == "" || s.inputActive || (s.transport != "" && s.transport != transport) {
		return false
	}
	s.transport = transport
	s.inputActive = true
	return true
}

func (s *sessionOwnership) releaseInput(transport string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transport != transport || !s.inputActive {
		return
	}
	s.inputActive = false
	s.clearIdleOwner()
}

func (s *sessionOwnership) reserveVideo(transport string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if transport == "" || (s.transport != "" && s.transport != transport) {
		return false
	}
	if s.videoWaiters > 0 || s.videoHandoffInProgress {
		return false
	}
	s.transport = transport
	s.videoWaiters++
	return true
}

func (s *sessionOwnership) activateReservedVideo(transport string) (bool, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transport != transport || s.videoWaiters == 0 {
		return false, nil
	}
	if s.videoActive {
		if s.videoHandoff == nil {
			s.videoHandoff = make(chan struct{})
		}
		return false, s.videoHandoff
	}
	s.videoWaiters--
	s.videoActive = true
	s.videoHandoff = nil
	return true, nil
}

func (s *sessionOwnership) finishVideoHandoff(transport string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transport != transport || !s.videoHandoffInProgress {
		return
	}
	s.videoHandoffInProgress = false
	s.videoHandoff = nil
}

func (s *sessionOwnership) cancelVideoReservation(transport string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transport != transport || s.videoWaiters == 0 {
		return
	}
	s.videoWaiters--
	if s.videoWaiters == 0 {
		s.videoHandoffInProgress = false
		s.videoHandoff = nil
	}
	s.clearIdleOwner()
}

func (s *sessionOwnership) releaseVideo(transport string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transport != transport || !s.videoActive {
		return
	}
	s.videoActive = false
	if s.videoHandoff != nil {
		close(s.videoHandoff)
		s.videoHandoffInProgress = true
	}
	s.clearIdleOwner()
}

func (s *sessionOwnership) currentTransport() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transport
}

func (s *sessionOwnership) videoReservationInfo() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transport, s.videoWaiters > 0 || s.videoHandoffInProgress
}

func (s *sessionOwnership) clearIdleOwner() {
	if !s.inputActive && !s.videoActive && s.videoWaiters == 0 {
		s.transport = ""
		s.videoHandoffInProgress = false
		s.videoHandoff = nil
	}
}
