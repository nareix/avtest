
package main

import (
	"bytes"
	"os"
	"io"
	"math"
	"path/filepath"
	"io/ioutil"
	"encoding/json"
	"encoding/binary"
	"encoding/hex"
	"github.com/nareix/av"
	"github.com/nareix/rtsp"
	"github.com/nareix/mp4"
	"github.com/nareix/ts"
	"github.com/nareix/codec"
	"github.com/nareix/codec/aacparser"
	"github.com/nareix/codec/h264parser"
	"github.com/nareix/mp4/atom"
	"fmt"
	"flag"
	"net/http"

	oldts "./oldts"
)

func testMp4Demux() {
	var err error

	outfile, _ := os.Create("out.mp4")
	defer outfile.Close()

	outfile2, _ := os.Create("out.ts")
	defer outfile2.Close()

	//infile, _ := os.Open("projectindex.mp4")
	//demuxer := &mp4.Demuxer{R: infile}
	demuxer := &FFDemuxer{Filename: "projectindex-0-bp.mp4"}

	demuxer.ReadHeader()
	streams := demuxer.Streams()

	type Packet struct {
		av.Packet
		i int
	}
	pkts := []Packet{}

	muxer := &mp4.Muxer{W: outfile}
	muxer2 := &ts.Muxer{W: outfile2}

	setStreams := func(muxer av.Muxer) {
		for _, stream := range streams {
			ns := muxer.NewStream()
			ns.FillParamsByStream(stream)
		}
	}

	setStreams(muxer)
	setStreams(muxer2)
	err = muxer.WriteHeader()
	fmt.Println(err)
	err = muxer2.WriteHeader()
	fmt.Println(err)

	fmt.Println(streams)
	//fmt.Println(demuxer.SeekToTime(14.3*60.0))

	gop := 0
	totalTime := float64(0.0)
	for {
		i, pkt, err := demuxer.ReadPacket()
		if err != nil {
			break
		}
		if streams[i].IsVideo() && pkt.IsKeyFrame {
			gop++
		}
		if gop == 2 {
			break
		}
		fmt.Println(streams[i].Type(), fmt.Sprintf("ts=%.2f dur=%.3f cts=%.3f", demuxer.Time(), pkt.Duration, pkt.CompositionTime),
			pkt.IsKeyFrame, len(pkt.Data), fmt.Sprintf("%x", pkt.Data[:4]))

		if streams[i].IsVideo() {
			totalTime += pkt.Duration
		}

		pkts = append(pkts, Packet{i:i, Packet:pkt})
	}

	for i := 0; i < 3; i++ {
		for _, pkt := range pkts {
			muxer.WritePacket(pkt.i, pkt.Packet)
			muxer2.WritePacket(pkt.i, pkt.Packet)
		}
	}

	err = muxer.WriteTrailer()
	fmt.Println(err)

	indexfile, _ := os.Create("projectindex.m3u8")
	writeM3U8Header(indexfile, totalTime+1)

	for i := 0; i < 10; i++ {
		filename := fmt.Sprintf("projectindex.hls.%d.ts", i)

		writeTs := func() {
			file, _ := os.Create(filename)
			muxer := &ts.Muxer{W: file}
			setStreams(muxer)
			muxer.WriteHeader()
			for _, pkt := range pkts {
				muxer.WritePacket(pkt.i, pkt.Packet)
			}
			file.Close()
		}

		writeOldTs := func() {
			oldtsfile, _ := os.Create(filename)
			oldmux := oldts.Muxer{W: oldtsfile}
			timeScaleH264 := int64(90000)
			timeScaleAAC := int64(10000)

			h264info, _ := h264parser.ParseCodecData(streams[0].CodecData())
			old264 := oldmux.AddH264Track()
			old264.SetTimeScale(timeScaleH264)
			old264.SetH264PPSAndSPS(h264info.Record.PPS[0], h264info.Record.SPS[0])
			dts264 := int64(0)

			aacinfo, _ := aacparser.ParseCodecData(streams[1].CodecData())
			oldaac := oldmux.AddAACTrack()
			oldaac.SetMPEG4AudioConfig(aacinfo.MPEG4AudioConfig)
			oldaac.SetTimeScale(timeScaleAAC)
			dtsaac := int64(0)

			fmt.Println("start mux")
			oldmux.WriteHeader()
			for _, pkt := range pkts {
				if pkt.i == 0 {
					dts264 += int64(pkt.Duration*float64(timeScaleH264))
					pts := dts264+int64(pkt.CompositionTime*float64(timeScaleH264))
					old264.WriteSample(pts, dts264, pkt.IsKeyFrame, pkt.Data)
					fmt.Printf("h264 pts=%d dts=%d\n", pts, dts264)
				} else {
					dtsaac += int64(pkt.Duration*float64(timeScaleAAC))
					pts := dtsaac+int64(pkt.CompositionTime*float64(timeScaleAAC))
					oldaac.WriteSample(pts, dtsaac, pkt.IsKeyFrame, pkt.Data)
					fmt.Printf("aac pts=%d dts=%d\n", pts, dtsaac)
				}
			}
			oldtsfile.Close()
		}

		if true {
			writeOldTs()
		} else {
			writeTs()
		}

		writeM3U8Item(indexfile, filename, totalTime)
		dumpTs(filename)
	}

	writeM3U8Footer(indexfile)
	indexfile.Close()

}

func rearrangeUglyTsFilesMakeM3u8(filenames []string, m3u8path string) {
	type Packet struct {
		av.Packet
		Index int
		Type av.CodecType
	}
	type Gop struct {
		pkts [][]*Packet
	}
	pkts := []*Packet{}
	gops := []Gop{}

	var streams []av.Stream
	var vidx, aidx int

	for _, filename := range filenames {
		file, _ := os.Open(filename)
		demuxer := &ts.Demuxer{R: file}
		demuxer.ReadHeader()
		streams = demuxer.Streams()
		fmt.Println(filename, streams)
		for {
			i, pkt, err := demuxer.ReadPacket()
			if err != nil {
				break
			}
			if streams[i].Type() == av.AAC {
				frame := pkt.Data
				for len(frame) > 0 {
					if config, payload, samples, framelen, err := aacparser.ReadADTSFrame(frame); err != nil {
						panic(err)
					} else {
						config = config.Complete()
						dur := float64(samples)/float64(config.SampleRate)
						frame = frame[framelen:]
						pkts = append(pkts, &Packet{
							Index: i,
							Packet: av.Packet{Data: payload, Duration: dur},
							Type: streams[i].Type(),
						})
					}
				}
			} else {
				pkt.Duration = float64(1.0)/float64(25.0)
				pkts = append(pkts, &Packet{
					Index: i,
					Packet: pkt,
					Type: streams[i].Type(),
				})
			}
		}
	}

	for i := range streams {
		if streams[i].Type() == av.H264 {
			vidx = i
		} else {
			aidx = i
		}
	}

	var gop *Gop
	for _, pkt := range pkts {
		if pkt.Type == av.H264 {
			if pkt.IsKeyFrame {
				gops = append(gops, Gop{
					pkts: make([][]*Packet, len(streams)),
				})
				gop = &gops[len(gops)-1]
			}
		}
		if gop != nil {
			gop.pkts[pkt.Index] = append(gop.pkts[pkt.Index], pkt)
		}
	}

	maxgopdur := float64(0)
	for i, gop := range gops {
		h264dur := float64(0)
		h264nr := 0
		aacdur := float64(0)

		for i, pkts := range gop.pkts {
			for _, pkt := range pkts {
				if streams[i].Type() == av.H264 {
					h264nr++
					h264dur += pkt.Duration
				} else {
					aacdur += pkt.Duration
				}
			}
		}

		fmt.Printf("gop#%d aac.nr=%d h264.nr=%d aacdur=%.2f h264dur=%.2f\n",
				i,
				len(gop.pkts[aidx]),
				len(gop.pkts[vidx]),
				aacdur, h264dur)
		if h264dur > maxgopdur {
			maxgopdur = h264dur
		}
		if aacdur > maxgopdur {
			maxgopdur = aacdur
		}

		for i, pkts := range gop.pkts {
			for _, pkt := range pkts {
				if streams[i].Type() == av.H264 {
					pkt.Duration = h264dur/float64(h264nr)
				}
			}
		}
	}

	indexfile, _ := os.Create(m3u8path)
	writeM3U8Header(indexfile, maxgopdur)

	for i, gop := range gops {
		filename := m3u8path+fmt.Sprintf(".%d.ts", i)
		outfile, _ := os.Create(filename)
		muxer := &ts.Muxer{
			W: outfile,
			PaddingToMakeCounterCont: true,
		}
		for _, stream := range streams {
			ns := muxer.NewStream()
			ns.FillParamsByStream(stream)
		}
		muxer.WriteHeader()
		fmt.Println("write: gop", i)

		time := make([]float64, len(streams))
		pos := make([]int, len(streams))
		for {
			i := -1
			if time[aidx] <= time[vidx] && pos[aidx] < len(gop.pkts[aidx]) {
				i = aidx
			} else if time[vidx] < time[aidx] && pos[vidx] < len(gop.pkts[vidx]) {
				i = vidx
			} else if pos[aidx] < len(gop.pkts[aidx]) {
				i = aidx
			} else if pos[vidx] < len(gop.pkts[vidx]) {
				i = vidx
			} else {
				break
			}
			//fmt.Println("write: ", streams[i].Type())
			pkt := gop.pkts[i][pos[i]].Packet
			muxer.WritePacket(i, pkt)
			time[i] += pkt.Duration
			pos[i]++
		}
		muxer.WriteTrailer()

		outfile.Close()
		maxtime := time[0]
		for _, t := range time {
			if t > maxtime {
				maxtime = t
			}
		}
		dumpTs(filename)
		writeM3U8Item(indexfile, filepath.Base(filename), maxtime)
	}
	writeM3U8Footer(indexfile)
	indexfile.Close()

	fmt.Println("gops", len(gops))
}

func rearrangeUglyTs(filename string) {
	file, _ := os.Open(filename)
	logfile, _ := os.Create(filename+".rearrange.log")
	demuxer := &ts.Demuxer{R: file}
	demuxer.ReadHeader()
	streams := demuxer.Streams()

	type tsPacket struct {
		av.Packet
		Index int
	}
	pkts := []*tsPacket{}

	for {
		i, pkt, err := demuxer.ReadPacket()
		if err != nil {
			break
		}
		pkts = append(pkts, &tsPacket{Index:i, Packet:pkt})
	}

	aacdur := float64(0.0)
	h264dur := float64(0.0)
	aacnr := 0
	h264nr := 0

	fmt.Fprintln(logfile, "=== before rearrange ===")
	for _, pkt := range pkts {
		if streams[pkt.Index].Type() == av.AAC {
			aacdur += pkt.Duration
			aacnr++
		} else {
			h264dur += pkt.Duration
			h264nr++
		}
	}
	fmt.Fprintln(logfile, "aacdur", aacdur, "aacnr", aacnr)
	fmt.Fprintln(logfile, "h264dur", h264dur, "h264nr", h264nr)

	for _, pkt := range pkts {
		i := pkt.Index
		if streams[i].Type() == av.AAC {
			_, _, samples, err := aacparser.ExtractADTSFrames(pkt.Data)
			if err != nil {
				panic(err)
			}
			dur := float64(samples)/float64(streams[i].SampleRate())
			fmt.Fprintf(logfile, "%v dur=%.3f diff=%.3f len=%d\n",
				streams[i].Type(), pkt.Duration, pkt.Duration-dur, len(pkt.Data))

			if _, _, samples, err := aacparser.ExtractADTSFrames(pkt.Data); err != nil {
				panic(err)
			} else {
				dur := float64(samples)/float64(streams[i].SampleRate())
				pkt.Duration = dur
			}
		} else {
			fmt.Fprintf(logfile, "%v dur=%.3f key=%v len=%d\n",
				streams[i].Type(), pkt.Duration, pkt.IsKeyFrame, len(pkt.Data))
			pkt.Duration = float64(h264dur)/float64(h264nr)
		}
	}

	fmt.Fprintln(logfile, "=== after rearrange ===")
	aacdur = float64(0.0)
	h264dur = float64(0.0)
	for _, pkt := range pkts {
		if streams[pkt.Index].Type() == av.AAC {
			aacdur += pkt.Duration
		} else {
			h264dur += pkt.Duration
		}
	}
	fmt.Fprintln(logfile, "aacdur", aacdur)
	fmt.Fprintln(logfile, "h264dur", h264dur)

	outfile, _ := os.Create(filename+".rearrange.ts")
	muxer := &ts.Muxer{
		W: outfile,
		PaddingToMakeCounterCont: true,
	}
	for _, stream := range streams {
		ns := muxer.NewStream()
		ns.FillParamsByStream(stream)
	}
	muxer.WriteHeader()
	for _, pkt := range pkts {
		muxer.WritePacket(pkt.Index, pkt.Packet)
	}
	muxer.WriteTrailer()
	outfile.Close()

	logfile.Close()
}

func dumpFragMp4(filename string) {
	file, _ := os.Open(filename)
	dumpfile, _ := os.Create(filename+".dumpfrag.log")
	defer dumpfile.Close()

	type Entry struct {
		Start,End int64
	}
	type Output struct {
		InitSegEnd int64
		Entries []Entry
	}
	var output Output

	dumper := &atom.Dumper{W: dumpfile}
	var posStart, posEnd, initSegEnd int64
	for {
		rd, cc4, err := atom.ReadAtomHeader(file, "")
		if err != nil {
			break
		}

		if cc4 == "moof" {
			posStart, _ = file.Seek(0, 1)
			posStart -= 8
			frag, _ := atom.ReadMovieFrag(rd)
			if frag.Tracks[0].Header.Id < 3 {
				atom.WalkMovieFrag(dumper, frag)
			}
		} else if cc4 == "moov" {
			moov, _ := atom.ReadMovie(rd)
			atom.WalkMovie(dumper, moov)
			initSegEnd, _ = file.Seek(0, 1)
		} else {
			io.CopyN(ioutil.Discard, rd, rd.N)
			if cc4 == "mdat" {
				posEnd, _ = file.Seek(0, 1)
				output.Entries = append(output.Entries, Entry{posStart,posEnd})
			}
		}
	}

	output.InitSegEnd = initSegEnd
	outfile, _ := os.Create(filename+".fraginfo.json")
	json.NewEncoder(outfile).Encode(output)
	outfile.Close()
}

func dumpTs(filename string) {
	dumpfile, _ := os.Create(filename+".dumpts.log")
	ts.DebugReader = true
	ts.DebugOutput = dumpfile
	file, err := os.Open(filename)
	demuxer := ts.Demuxer{R: file}

	err = demuxer.ReadHeader()
	streams := demuxer.Streams()
	fmt.Fprintln(dumpfile, streams, err)
	aacTotalDur := float64(0)

	for {
		i, pkt, err := demuxer.ReadPacket()
		if err != nil {
			break
		}
		if streams[i].Type() == av.AAC {
			if _, _, samples, err := aacparser.ExtractADTSFrames(pkt.Data); err != nil {
				panic(err)
			} else {
				dur := float64(samples)/float64(streams[i].SampleRate())
				aacTotalDur += dur
			}
		}
		fmt.Fprintln(dumpfile, streams[i].Type(), fmt.Sprintf("ts=%.2f dur=%.3f cts=%.3f", demuxer.Time(), pkt.Duration, pkt.CompositionTime),
			pkt.IsKeyFrame, len(pkt.Data), fmt.Sprintf("%x", pkt.Data[:4]))
	}
	fmt.Fprintln(dumpfile, "aacTotalDur", aacTotalDur)

	dumpfile.Close()
}

func testRtsp(uri string) {
	cli, err := rtsp.Connect(uri)
	if err != nil {
		panic(err)
	}

	cli.DebugConn = true
	streams, err := cli.Describe()
	if err != nil {
		panic(err)
	}

	setup := []int{}
	for i := range streams {
		setup = append(setup, i)
	}

	err = cli.Setup(setup)
	if err != nil {
		panic(err)
	}

	err = cli.Play()
	if err != nil {
		panic(err)
	}

	for {
		si, pkt, err := cli.ReadPacket()
		if err != nil {
			panic(err)
		}
		if true && si == 0 {
			sliceType, err := h264parser.ParseSliceHeaderFromNALU(pkt.Data)
			if false && err == nil {
				fmt.Print(sliceType, " ")
			}
			if false {
				fmt.Println(hex.Dump(pkt.Data[:16]))
			}
		}
	}
}

func testAACEnc(filename string) {
	enc := codec.FindAudioEncoderByName("aac")
	enc.SampleFormat = codec.FLTP
	enc.SampleRate = 8000
	enc.ChannelCount = 1
	enc.BitRate = 50000

	if err := enc.Setup(); err != nil {
		panic(err)
	}

	config, err := aacparser.ParseCodecData(enc.Extradata())
	if err != nil {
		panic(err)
	}
	fmt.Println("config", config)

	time := float64(0)
	sampleCount := 1024
	tincr := 2*math.Pi*440.0/float64(config.SampleRate)

	genbuf := func() []byte {
		rawdata := &bytes.Buffer{}
		for i := 0; i < sampleCount; i++ {
			val := float32(math.Sin(time))
			for j := 0; j < config.ChannelCount; j++ {
				binary.Write(rawdata, binary.LittleEndian, val)
			}
			time += tincr
		}
		return rawdata.Bytes()
	}

	file, _ := os.Create(filename)
	for i := 0; i < config.SampleRate/sampleCount; i++ {
		buf := genbuf()
		got, frame, err := enc.Encode(buf, false)
		if err != nil {
			panic(err)
		}
		if got {
			adtshdr := aacparser.MakeADTSHeader(config.MPEG4AudioConfig, sampleCount, len(frame))
			file.Write(adtshdr)
			file.Write(frame)
			fmt.Println("buf", len(buf), "frame", len(frame), err)
		}
	}
	for i := 0; i < 10; i++ {
		got, pkt, err := enc.Encode([]byte{}, true)
		fmt.Println("gotlastpkt?", got, err, len(pkt))
		if !got {
			break
		}
	}

	file.Close()
}

func main() {
	dumpts := flag.Bool("dumpts", false, "dump ts file info")
	dumpfrag := flag.String("dumpfrag", "", "dump fragment mp4 info")
	rearrangeuglyts := flag.Bool("rearrangeuglyts", false, "rearrange ugly ts")
	rearrangeuglytsmakem3u8 := flag.String("rearrangeuglytsmakem3u8", "", "rearrange ugly ts and make m3u8")
	httpserver := flag.String("httpserver", "", "server http")
	test := flag.Bool("test", false, "test")
	testrtsp := flag.String("testrtsp", "", "test rtsp")
	testaacenc := flag.String("testaacenc", "", "test aac encoder")
	flag.Parse()

	// Stream #0:1: Audio: pcm_mulaw, 8000 Hz, 1 channels, s16, 64 kb/s
	if *testaacenc != "" {
		testAACEnc(*testaacenc)
	}

	if *testrtsp != "" {
		testRtsp(*testrtsp)
	}

	if *rearrangeuglytsmakem3u8 != "" {
		rearrangeUglyTsFilesMakeM3u8(flag.Args(), *rearrangeuglytsmakem3u8)
	}

	if *rearrangeuglyts {
		for _, filename := range flag.Args() {
			rearrangeUglyTs(filename)
		}
	}

	if *dumpts {
		for _, filename := range flag.Args(){
			dumpTs(filename)
		}
	}

	if *dumpfrag != "" {
		dumpFragMp4(*dumpfrag)
	}

	if *test {
		testMp4Demux()
	}

	if *httpserver != "" {
		http.ListenAndServe(*httpserver, http.FileServer(http.Dir(".")))
	}
}

