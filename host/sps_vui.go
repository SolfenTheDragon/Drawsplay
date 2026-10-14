package main

// GStreamer's VA H.264 encoders write a VUI without video_signal_type, leaving
// the colour range and matrix of the stream unspecified. The tablet's decoder
// then has to guess, and a wrong guess (full vs limited range) stretches or
// crushes the 8-bit levels, which shows as banding in gradients and dark
// scenes. withBT709LimitedVUI rewrites such an SPS to declare what the
// pipelines actually produce: BT.709 primaries/transfer/matrix, limited range
// (the same tags the X11 ffmpeg path writes via out_range=tv).

import "errors"

type bitReader struct {
	data []byte
	pos  int
}

func (r *bitReader) bit() (uint32, error) {
	if r.pos >= len(r.data)*8 {
		return 0, errors.New("sps: truncated")
	}
	b := uint32(r.data[r.pos/8]>>(7-uint(r.pos%8))) & 1
	r.pos++
	return b, nil
}

func (r *bitReader) bits(n int) (uint32, error) {
	var v uint32
	for i := 0; i < n; i++ {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		v = v<<1 | b
	}
	return v, nil
}

func (r *bitReader) ue() (uint32, error) {
	zeros := 0
	for {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		zeros++
		if zeros > 31 {
			return 0, errors.New("sps: bad exp-golomb")
		}
	}
	v, err := r.bits(zeros)
	return (1<<uint(zeros) - 1) + v, err
}

func (r *bitReader) skipUE(n int) error {
	for i := 0; i < n; i++ {
		if _, err := r.ue(); err != nil {
			return err
		}
	}
	return nil
}

type bitWriter struct {
	data []byte
	n    int
}

func (w *bitWriter) bits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.data = append(w.data, 0)
		}
		if v>>uint(i)&1 == 1 {
			w.data[len(w.data)-1] |= 1 << (7 - uint(w.n%8))
		}
		w.n++
	}
}

func (w *bitWriter) ue(v uint32) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	w.bits(0, n)
	w.bits(v, n+1)
}

func (w *bitWriter) copyFrom(r *bitReader, from, to int) {
	for p := from; p < to; p++ {
		w.bits(uint32(r.data[p/8]>>(7-uint(p%8)))&1, 1)
	}
}

func unescapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

func escapeRBSP(b []byte) []byte {
	out := make([]byte, 0, len(b)+8)
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

func skipScalingList(r *bitReader, size int) error {
	last, next := int32(8), int32(8)
	for j := 0; j < size; j++ {
		if next != 0 {
			v, err := r.ue()
			if err != nil {
				return err
			}
			delta := int32((v + 1) / 2)
			if v%2 == 0 {
				delta = -delta
			}
			next = (last + delta + 256) % 256
		}
		if next != 0 {
			last = next
		}
	}
	return nil
}

// writeBT709LimitedSignalType writes video_signal_type_present_flag=1 and its
// fields: video_format=5 (unspecified), video_full_range_flag=0, and a colour
// description of BT.709 (1) for primaries, transfer and matrix.
func writeBT709LimitedSignalType(w *bitWriter) {
	w.bits(1, 1)
	w.bits(5, 3)
	w.bits(0, 1)
	w.bits(1, 1)
	w.bits(1, 8)
	w.bits(1, 8)
	w.bits(1, 8)
}

// withBT709LimitedVUI returns nal (an Annex-B SPS NAL unit with start code)
// rewritten to carry a BT.709 limited-range video_signal_type, or nal itself
// when it is not an H.264 SPS, already signals its colour, or cannot be parsed.
func withBT709LimitedVUI(nal []byte) []byte {
	h := 0
	for h < len(nal) && nal[h] == 0 {
		h++
	}
	if h < 2 || h >= len(nal) || nal[h] != 1 || h+1 >= len(nal) || nal[h+1]&0x1F != 7 {
		return nal
	}
	h++
	out, err := rewriteSPSPayload(unescapeRBSP(nal[h+1:]))
	if err != nil || out == nil {
		return nal
	}
	res := make([]byte, 0, h+1+len(out)+8)
	res = append(res, nal[:h+1]...)
	return append(res, escapeRBSP(out)...)
}

func rewriteSPSPayload(rbsp []byte) ([]byte, error) {
	r := &bitReader{data: rbsp}
	profile, err := r.bits(8)
	if err != nil {
		return nil, err
	}
	if _, err := r.bits(16); err != nil {
		return nil, err
	}
	if err := r.skipUE(1); err != nil {
		return nil, err
	}
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma, err := r.ue()
		if err != nil {
			return nil, err
		}
		if chroma == 3 {
			if _, err := r.bit(); err != nil {
				return nil, err
			}
		}
		if err := r.skipUE(2); err != nil {
			return nil, err
		}
		if _, err := r.bit(); err != nil {
			return nil, err
		}
		present, err := r.bit()
		if err != nil {
			return nil, err
		}
		if present == 1 {
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			for i := 0; i < lists; i++ {
				f, err := r.bit()
				if err != nil {
					return nil, err
				}
				if f == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					if err := skipScalingList(r, size); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if err := r.skipUE(1); err != nil {
		return nil, err
	}
	pocType, err := r.ue()
	if err != nil {
		return nil, err
	}
	switch pocType {
	case 0:
		if err := r.skipUE(1); err != nil {
			return nil, err
		}
	case 1:
		if _, err := r.bit(); err != nil {
			return nil, err
		}
		if err := r.skipUE(2); err != nil {
			return nil, err
		}
		n, err := r.ue()
		if err != nil {
			return nil, err
		}
		if n > 255 {
			return nil, errors.New("sps: bad poc cycle")
		}
		if err := r.skipUE(int(n)); err != nil {
			return nil, err
		}
	}
	if err := r.skipUE(1); err != nil {
		return nil, err
	}
	if _, err := r.bit(); err != nil {
		return nil, err
	}
	if err := r.skipUE(2); err != nil {
		return nil, err
	}
	frameMbsOnly, err := r.bit()
	if err != nil {
		return nil, err
	}
	if frameMbsOnly == 0 {
		if _, err := r.bit(); err != nil {
			return nil, err
		}
	}
	if _, err := r.bit(); err != nil {
		return nil, err
	}
	crop, err := r.bit()
	if err != nil {
		return nil, err
	}
	if crop == 1 {
		if err := r.skipUE(4); err != nil {
			return nil, err
		}
	}

	w := &bitWriter{}
	vuiPos := r.pos
	vui, err := r.bit()
	if err != nil {
		return nil, err
	}
	if vui == 0 {
		w.copyFrom(r, 0, vuiPos)
		w.bits(1, 1) // vui_parameters_present_flag
		w.bits(0, 1) // aspect_ratio_info_present_flag
		w.bits(0, 1) // overscan_info_present_flag
		writeBT709LimitedSignalType(w)
		w.bits(0, 1) // chroma_loc_info_present_flag
		w.bits(0, 1) // timing_info_present_flag
		w.bits(0, 1) // nal_hrd_parameters_present_flag
		w.bits(0, 1) // vcl_hrd_parameters_present_flag
		w.bits(0, 1) // pic_struct_present_flag
		w.bits(0, 1) // bitstream_restriction_flag
		w.bits(1, 1) // rbsp_stop_one_bit
		return w.data, nil
	}
	aspect, err := r.bit()
	if err != nil {
		return nil, err
	}
	if aspect == 1 {
		idc, err := r.bits(8)
		if err != nil {
			return nil, err
		}
		if idc == 255 {
			if _, err := r.bits(32); err != nil {
				return nil, err
			}
		}
	}
	overscan, err := r.bit()
	if err != nil {
		return nil, err
	}
	if overscan == 1 {
		if _, err := r.bit(); err != nil {
			return nil, err
		}
	}
	sigPos := r.pos
	sig, err := r.bit()
	if err != nil {
		return nil, err
	}
	if sig == 1 {
		return nil, nil
	}
	w.copyFrom(r, 0, sigPos)
	writeBT709LimitedSignalType(w)
	w.copyFrom(r, r.pos, len(rbsp)*8)
	// The copied tail ends with rbsp_stop_one_bit plus alignment zeros; the
	// insertion shifted it by 26 bits, so trim whole zero bytes it now spills.
	out := w.data
	for len(out) > 0 && out[len(out)-1] == 0 {
		out = out[:len(out)-1]
	}
	return out, nil
}

// spsVUICache memoizes withBT709LimitedVUI for the (normally unchanging) SPS
// an encoder repeats before every IDR.
type spsVUICache struct {
	in, out []byte
}

func (c *spsVUICache) rewrite(nal []byte) []byte {
	if c.in == nil || string(c.in) != string(nal) {
		c.in = append([]byte(nil), nal...)
		c.out = withBT709LimitedVUI(c.in)
	}
	return c.out
}
