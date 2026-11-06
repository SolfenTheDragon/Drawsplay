package h264

// GStreamer's VA H.264 encoders write a VUI without video_signal_type, leaving
// the colour range and matrix of the stream unspecified. The tablet's decoder
// then has to guess, and a wrong guess (full vs limited range) stretches or
// crushes the 8-bit levels, which shows as banding in gradients and dark
// scenes. WithBT709LimitedVUI rewrites such an SPS to declare what the
// pipelines actually produce: BT.709 primaries/transfer/matrix, limited range
// (the same tags the X11 ffmpeg path writes via out_range=tv).

// SPSDimensions returns the cropped picture size of a progressive baseline SPS
// that carries a 3- or 4-byte start code. The tablet requires the Wi-Fi config
// size to equal this, so it is derived from the stream instead of being assumed.
func SPSDimensions(sps []byte) (width, height int, err error) {
	sps = sps[StartCodeLen(sps):]
	if len(sps) < 5 || sps[0]&0x1f != TypeSPS || sps[1] != 66 {
		return 0, 0, errDimensions
	}
	r := NewBitReader(Unescape(sps[4:]))
	// Any read error (a truncated SPS) sets failed; the result is then refused.
	failed := false
	ue := func() uint32 {
		v, err := r.UE()
		failed = failed || err != nil
		return v
	}
	bit := func() uint32 {
		v, err := r.Bit()
		failed = failed || err != nil
		return v
	}
	ue() // seq_parameter_set_id
	ue() // log2_max_frame_num_minus4
	switch ue() {
	case 0:
		ue() // log2_max_pic_order_cnt_lsb_minus4
	case 2:
	default:
		return 0, 0, errDimensions
	}
	ue()  // max_num_ref_frames
	bit() // gaps_in_frame_num_value_allowed_flag
	widthMB, heightMB := ue()+1, ue()+1
	if bit() != 1 { // frame_mbs_only_flag
		return 0, 0, errDimensions
	}
	bit() // direct_8x8_inference_flag
	var left, right, top, bottom uint32
	if bit() == 1 {
		left, right, top, bottom = ue(), ue(), ue(), ue()
	}
	if failed || widthMB > 511 || heightMB > 511 || (left+right)*2 >= widthMB*16 || (top+bottom)*2 >= heightMB*16 {
		return 0, 0, errDimensions
	}
	return int(widthMB*16 - (left+right)*2), int(heightMB*16 - (top+bottom)*2), nil
}

func skipScalingList(r *BitReader, size int) error {
	last, next := int32(8), int32(8)
	for j := 0; j < size; j++ {
		if next != 0 {
			v, err := r.UE()
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
func writeBT709LimitedSignalType(w *BitWriter) {
	w.Bits(1, 1)
	w.Bits(5, 3)
	w.Bits(0, 1)
	w.Bits(1, 1)
	w.Bits(1, 8)
	w.Bits(1, 8)
	w.Bits(1, 8)
}

// WithBT709LimitedVUI returns nal (an Annex-B SPS NAL unit with start code)
// rewritten to carry a BT.709 limited-range video_signal_type, or nal itself
// when it is not an H.264 SPS, already signals its colour, or cannot be parsed.
func WithBT709LimitedVUI(nal []byte) []byte {
	h := 0
	for h < len(nal) && nal[h] == 0 {
		h++
	}
	if h < 2 || h >= len(nal) || nal[h] != 1 || h+1 >= len(nal) || nal[h+1]&0x1F != 7 {
		return nal
	}
	h++
	out, err := rewriteSPSPayload(Unescape(nal[h+1:]))
	if err != nil || out == nil {
		return nal
	}
	res := make([]byte, 0, h+1+len(out)+8)
	res = append(res, nal[:h+1]...)
	return append(res, Escape(out)...)
}

func rewriteSPSPayload(rbsp []byte) ([]byte, error) {
	r := NewBitReader(rbsp)
	profile, err := r.Bits(8)
	if err != nil {
		return nil, err
	}
	if _, err := r.Bits(16); err != nil {
		return nil, err
	}
	if err := r.SkipUE(1); err != nil {
		return nil, err
	}
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chroma, err := r.UE()
		if err != nil {
			return nil, err
		}
		if chroma == 3 {
			if _, err := r.Bit(); err != nil {
				return nil, err
			}
		}
		if err := r.SkipUE(2); err != nil {
			return nil, err
		}
		if _, err := r.Bit(); err != nil {
			return nil, err
		}
		present, err := r.Bit()
		if err != nil {
			return nil, err
		}
		if present == 1 {
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			for i := 0; i < lists; i++ {
				f, err := r.Bit()
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
	if err := r.SkipUE(1); err != nil {
		return nil, err
	}
	pocType, err := r.UE()
	if err != nil {
		return nil, err
	}
	switch pocType {
	case 0:
		if err := r.SkipUE(1); err != nil {
			return nil, err
		}
	case 1:
		if _, err := r.Bit(); err != nil {
			return nil, err
		}
		if err := r.SkipUE(2); err != nil {
			return nil, err
		}
		n, err := r.UE()
		if err != nil {
			return nil, err
		}
		if n > 255 {
			return nil, errPOCCycle
		}
		if err := r.SkipUE(int(n)); err != nil {
			return nil, err
		}
	}
	if err := r.SkipUE(1); err != nil {
		return nil, err
	}
	if _, err := r.Bit(); err != nil {
		return nil, err
	}
	if err := r.SkipUE(2); err != nil {
		return nil, err
	}
	frameMbsOnly, err := r.Bit()
	if err != nil {
		return nil, err
	}
	if frameMbsOnly == 0 {
		if _, err := r.Bit(); err != nil {
			return nil, err
		}
	}
	if _, err := r.Bit(); err != nil {
		return nil, err
	}
	crop, err := r.Bit()
	if err != nil {
		return nil, err
	}
	if crop == 1 {
		if err := r.SkipUE(4); err != nil {
			return nil, err
		}
	}

	w := &BitWriter{}
	vuiPos := r.pos
	vui, err := r.Bit()
	if err != nil {
		return nil, err
	}
	if vui == 0 {
		w.CopyFrom(r, 0, vuiPos)
		w.Bits(1, 1) // vui_parameters_present_flag
		w.Bits(0, 1) // aspect_ratio_info_present_flag
		w.Bits(0, 1) // overscan_info_present_flag
		writeBT709LimitedSignalType(w)
		w.Bits(0, 1) // chroma_loc_info_present_flag
		w.Bits(0, 1) // timing_info_present_flag
		w.Bits(0, 1) // nal_hrd_parameters_present_flag
		w.Bits(0, 1) // vcl_hrd_parameters_present_flag
		w.Bits(0, 1) // pic_struct_present_flag
		w.Bits(0, 1) // bitstream_restriction_flag
		w.Bits(1, 1) // rbsp_stop_one_bit
		return w.Bytes(), nil
	}
	aspect, err := r.Bit()
	if err != nil {
		return nil, err
	}
	if aspect == 1 {
		idc, err := r.Bits(8)
		if err != nil {
			return nil, err
		}
		if idc == 255 {
			if _, err := r.Bits(32); err != nil {
				return nil, err
			}
		}
	}
	overscan, err := r.Bit()
	if err != nil {
		return nil, err
	}
	if overscan == 1 {
		if _, err := r.Bit(); err != nil {
			return nil, err
		}
	}
	sigPos := r.pos
	sig, err := r.Bit()
	if err != nil {
		return nil, err
	}
	if sig == 1 {
		return nil, nil
	}
	w.CopyFrom(r, 0, sigPos)
	writeBT709LimitedSignalType(w)
	w.CopyFrom(r, r.pos, len(rbsp)*8)
	// The copied tail ends with rbsp_stop_one_bit plus alignment zeros; the
	// insertion shifted it by 26 bits, so trim whole zero bytes it now spills.
	out := w.Bytes()
	for len(out) > 0 && out[len(out)-1] == 0 {
		out = out[:len(out)-1]
	}
	return out, nil
}

// VUICache memoizes WithBT709LimitedVUI for the (normally unchanging) SPS
// an encoder repeats before every IDR.
type VUICache struct {
	in, out []byte
}

func (c *VUICache) Rewrite(nal []byte) []byte {
	if c.in == nil || string(c.in) != string(nal) {
		c.in = append([]byte(nil), nal...)
		c.out = WithBT709LimitedVUI(c.in)
	}
	return c.out
}
