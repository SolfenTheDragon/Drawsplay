package wifilan

import (
	"bytes"
	"errors"
)

var errSPSDimensions = errors.New("unsupported or truncated SPS; cannot read picture size")

// SPSDimensions returns the cropped picture size of a progressive baseline SPS
// that carries a 3- or 4-byte start code. The tablet requires the config size
// to equal this, so it is derived from the stream instead of being assumed.
func SPSDimensions(sps []byte) (width, height int, err error) {
	switch {
	case bytes.HasPrefix(sps, []byte{0, 0, 0, 1}):
		sps = sps[4:]
	case bytes.HasPrefix(sps, []byte{0, 0, 1}):
		sps = sps[3:]
	}
	if len(sps) < 5 || sps[0]&0x1f != 7 || sps[1] != 66 {
		return 0, 0, errSPSDimensions
	}
	rbsp := make([]byte, 0, len(sps))
	zeros := 0
	for _, b := range sps[4:] {
		if zeros >= 2 && b == 3 {
			zeros = 0
			continue
		}
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
		rbsp = append(rbsp, b)
	}
	r := spsBits{data: rbsp}
	ue := func() uint32 { v, _ := r.ue(); return v }
	ue() // seq_parameter_set_id
	ue() // log2_max_frame_num_minus4
	switch ue() {
	case 0:
		ue() // log2_max_pic_order_cnt_lsb_minus4
	case 2:
	default:
		return 0, 0, errSPSDimensions
	}
	ue()   // max_num_ref_frames
	r.u1() // gaps_in_frame_num_value_allowed_flag
	widthMB, heightMB := ue()+1, ue()+1
	if r.u1() != 1 { // frame_mbs_only_flag
		return 0, 0, errSPSDimensions
	}
	r.u1() // direct_8x8_inference_flag
	var left, right, top, bottom uint32
	if r.u1() == 1 {
		left, right, top, bottom = ue(), ue(), ue(), ue()
	}
	if r.err || widthMB > 511 || heightMB > 511 || (left+right)*2 >= widthMB*16 || (top+bottom)*2 >= heightMB*16 {
		return 0, 0, errSPSDimensions
	}
	return int(widthMB*16 - (left+right)*2), int(heightMB*16 - (top+bottom)*2), nil
}

type spsBits struct {
	data []byte
	pos  int
	err  bool
}

func (r *spsBits) u1() uint32 {
	if r.pos>>3 >= len(r.data) {
		r.err = true
		return 0
	}
	b := r.data[r.pos>>3] >> (7 - uint(r.pos&7)) & 1
	r.pos++
	return uint32(b)
}

func (r *spsBits) ue() (uint32, error) {
	zeros := 0
	for r.u1() == 0 {
		zeros++
		if r.err || zeros > 31 {
			r.err = true
			return 0, errSPSDimensions
		}
	}
	var v uint32
	for i := 0; i < zeros; i++ {
		v = v<<1 | r.u1()
	}
	return 1<<uint(zeros) - 1 + v, nil
}
