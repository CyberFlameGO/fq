package mpeg

// ffmpeg $(for i in $(seq 0 50); do echo "-f lavfi -i sine"; done) -t 100ms $(for i in $(seq 0 50); do echo "-map $i:0"; done) test2.ts

// ISO/IEC 13818-1 - Generic coding of moving pictures and associated audio information: Systems
// https://tsduck.io/download/docs/mpegts-introduction.pdf

// TODO: PSI reassemble, how know when done?
// TODO: probe, count?

import (
	"bytes"
	"fmt"

	"github.com/wader/fq/format"
	"github.com/wader/fq/pkg/bitio"
	"github.com/wader/fq/pkg/decode"
	"github.com/wader/fq/pkg/interp"
)

var mpegTsFormatMpegTsPacket decode.Group
var mpegTsFormatMpegTsPat decode.Group
var mpegTsFormatMpegTsPmt decode.Group

func init() {
	interp.RegisterFormat(decode.Format{
		Name:        format.MPEG_TS,
		Description: "MPEG Transport Stream",
		Groups:      []string{format.PROBE},
		Dependencies: []decode.Dependency{
			{Names: []string{format.MPEG_TS_PACKET}, Group: &mpegTsFormatMpegTsPacket},
			{Names: []string{format.MPEG_TS_PAT}, Group: &mpegTsFormatMpegTsPat},
			{Names: []string{format.MPEG_TS_PMT}, Group: &mpegTsFormatMpegTsPmt},
		},
		DecodeFn: tsDecode,
	})
}

const (
	adaptationFieldControlPayloadOnly               = 0b01
	adaptationFieldControlAdaptationFieldOnly       = 0b10
	adaptationFieldControlAdaptationFieldAndPayload = 0b11
)

type pesBuffer struct {
	buf bytes.Buffer
}

type tsStream struct {
	programPid int
	typ        int
}

type tsProgram struct {
	num        int
	pid        int
	streamPids []int
}

func tsDecode(d *decode.D, _ any) any {
	var pesReassemble = map[int]*pesBuffer{}
	pidProgramMap := map[int]format.MpegTsProgram{}
	pidStreamMap := map[int]format.MpegTsStream{}

	psiD := d.FieldArrayValue("psi")
	pesD := d.FieldArrayValue("pes")

	d.FieldArray("packets", func(d *decode.D) {
		for !d.End() {
			_, v := d.FieldFormat(
				"packet",
				mpegTsFormatMpegTsPacket,
				format.MpegTsPacketIn{
					ProgramMap: pidProgramMap,
					StreamMap:  pidStreamMap,
				},
			)
			mtpo, mtpoOk := v.(format.MpegTsPacketOut)
			if !mtpoOk {
				panic("packet is not a MpegTsPacketOut")
			}

			program, isPMT := pidProgramMap[mtpo.Pid]
			_, isES := pidStreamMap[mtpo.Pid]

			switch {
			case mtpo.Pid == pidPAT:

				// TODO: reset something? reassemble?
				_, v := psiD.FieldFormatBitBuf("packet", bitio.NewBitReader(mtpo.Payload, -1), mpegTsFormatMpegTsPat, nil)

				// _, v := d.FieldFormat("program_association_table", mpegTsFormatMpegTsPat, nil)

				mtpo, mtpoOk := v.(format.MpegTsPatOut)
				if !mtpoOk {
					panic(fmt.Sprintf("expected MpegTsPatOut got %#+v", v))
				}

				// TODO: correct? remove streams for program?
				for mapPid, mapNum := range mtpo.PidMap {
					if prevProgram, ok := pidProgramMap[mapPid]; ok {
						for _, streamPid := range prevProgram.StreamPids {
							delete(pidStreamMap, streamPid)
						}
					}
					pidProgramMap[mapPid] = format.MpegTsProgram{
						Number: mapNum,
						Pid:    mapPid,
					}
				}

			case isPMT:
				// TODO: reset something? reassemble?
				// _, v := d.FieldFormat("program_association_table", mpegTsFormatMpegTsPmt, nil)

				_, v := psiD.FieldFormatBitBuf("packet", bitio.NewBitReader(mtpo.Payload, -1), mpegTsFormatMpegTsPmt, nil)

				mtpo, mtpoOk := v.(format.MpegTsPmtOut)
				if !mtpoOk {
					panic(fmt.Sprintf("expected MpegTsPmtOut got %#+v", v))
				}

				// TODO: correct? replace streams?
				for _, streamPid := range program.StreamPids {
					delete(pidStreamMap, streamPid)
				}
				for streamPid, stream := range mtpo.Streams {
					program.StreamPids = append(program.StreamPids, streamPid)
					pidStreamMap[streamPid] = format.MpegTsStream{
						ProgramPid: program.Pid,
						Type:       stream.Type,
					}
				}

			case isES:
				// data := d.FieldRawLen("data", d.BitsLeft())
				// dataBytes := d.ReadAllBits(data)

				p, pOk := pesReassemble[mtpo.Pid]
				// log.Printf("pid=%d pOk=%t payloadUnitStart=%t len(dataBytes): %#+v\n", pid, pOk, payloadUnitStart, len(dataBytes))
				// if p != nil {
				// 	log.Printf("  p.buf.Len(): %#+v\n", p.buf.Len())
				// }

				if mtpo.PayloadUnitStart {
					// new packet starting
					if pOk {
						// log.Printf("  add %d", len(p.buf.Bytes()))
						// finish current seen pid
						pesD.FieldRootBitBuf("packet", bitio.NewBitReader(p.buf.Bytes(), -1))
					}
					p = &pesBuffer{}
					pesReassemble[mtpo.Pid] = p
				}
				if p != nil && len(mtpo.Payload) > 0 {
					p.buf.Write(mtpo.Payload)
				}
			}
		}
	})

	// TODO:
	// add possible partial pes
	for _, p := range pesReassemble {
		if p.buf.Len() == 0 {
			continue
		}
		pesD.FieldRootBitBuf("packet", bitio.NewBitReader(p.buf.Bytes(), -1))
	}

	return nil
}
