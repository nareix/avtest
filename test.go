
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"io"
	"io/ioutil"
	"github.com/nareix/ts"
	"github.com/nareix/mp4"
	"github.com/nareix/mp4/atom"
	"fmt"
	"encoding/hex"
	"encoding/gob"
	"runtime/pprof"
	"flag"
)

type GobAllSamples struct {
	TimeScale int
	SPS []byte
	PPS []byte
	Samples []GobSample
}

type GobSample struct {
	Duration int
	Data []byte
	Sync bool
}

type Stream struct {
	PID uint
	PESHeader *ts.PESHeader
	FirstTSHeader ts.TSHeader
	Title string
	Data bytes.Buffer
	Type uint
	PCR uint64
}

type Sample struct {
	Type uint
	PCR uint64
	PTS uint64
	DTS uint64
	Data []byte
	RandomAccessIndicator bool
}

var (
	debugData = true
	debugStream = true
	debugHeader = true
)

func readSamples(filename string, ch chan Sample) {
	defer func() {
		close(ch)
	}()

	var file *os.File
	var err error
	if file, err = os.Open(filename); err != nil {
		return
	}

	data := [188]byte{}

	var n int
	var header ts.TSHeader
	var pat ts.PAT
	var pmt ts.PMT
	var payload []byte
	var info ts.ElementaryStreamInfo
	streams := map[uint]*Stream{}

	findOrCreateStream := func(pid uint) (stream *Stream) {
		stream, _ = streams[pid]
		if stream == nil {
			stream = &Stream{
				PID: pid,
				Type: info.StreamType,
			}
			if stream.Type == ts.ElementaryStreamTypeH264 {
				stream.Title = "h264"
			} else if stream.Type == ts.ElementaryStreamTypeAdtsAAC {
				stream.Title = "aac"
			}
			streams[pid] = stream
		}
		return
	}

	onStreamPayloadUnitEnd := func(stream *Stream) {
		if debugStream {
			fmt.Printf("stream: %s end\n", stream.Title)
		}
		if debugData {
			fmt.Println(stream.Type, stream.Title, stream.Data.Len(), "total")
			if false {
				fmt.Println(hex.Dump(stream.Data.Bytes()))
			}
		}
		ch <- Sample{
			Type: stream.Type,
			Data: stream.Data.Bytes(),
			PTS: stream.PESHeader.PTS,
			DTS: stream.PESHeader.DTS,
			PCR: stream.FirstTSHeader.PCR,
			RandomAccessIndicator: stream.FirstTSHeader.RandomAccessIndicator,
		}
	}

	onStreamPayload := func() (err error) {
		stream := findOrCreateStream(header.PID)
		r := bytes.NewReader(payload)
		lr := &io.LimitedReader{R: r, N: int64(len(payload))}

		if header.PayloadUnitStart && stream.PESHeader != nil && stream.PESHeader.DataLength == 0 {
			onStreamPayloadUnitEnd(stream)
		}

		if header.PayloadUnitStart {
			stream.Data = bytes.Buffer{}
			if stream.PESHeader, err = ts.ReadPESHeader(lr); err != nil {
				return
			}
			stream.FirstTSHeader = header
			if debugStream {
				fmt.Printf("stream: %s start\n", stream.Title)
			}
		}

		if _, err = io.CopyN(&stream.Data, lr, lr.N); err != nil {
			return
		}
		if debugStream {
			fmt.Printf("stream: %s %d/%d\n", stream.Title, stream.Data.Len(), stream.PESHeader.DataLength)
		}

		if stream.Data.Len() == int(stream.PESHeader.DataLength) {
			onStreamPayloadUnitEnd(stream)
		}

		return
	}

	for {
		if header, n, err = ts.ReadTSPacket(file, data[:]); err != nil {
			return
		}
		if debugHeader {
			fmt.Println("header: ", header)
		}
		payload = data[:n]
		pr := bytes.NewReader(payload)

		if header.PID == 0 {
			if pat, err = ts.ReadPAT(pr); err != nil {
				return
			}
		}

		for _, entry := range(pat.Entries) {
			if entry.ProgramMapPID == header.PID {
				//fmt.Println("matchs", entry)
				if pmt, err = ts.ReadPMT(pr); err != nil {
					return
				}
				//fmt.Println("pmt", pmt)
			}
		}

		for _, info = range(pmt.ElementaryStreamInfos) {
			if info.ElementaryPID == header.PID {
				onStreamPayload()
			}
		}

	}
}

func writeM3U8Header(w io.Writer) {
	fmt.Fprintln(w, `#EXTM3U
#EXT-X-ALLOW-CACHE:YES
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-TARGETDURATION:9
#EXT-X-VERSION:3
#EXT-X-MEDIA-SEQUENCE:0`)
}

func writeM3U8Item(w io.Writer, filename string, size int64, duration float64) {
	fmt.Fprintf(w, `#EXT-X-BYTE-SIZE:%d
#EXTINF:%f,
%s
`, size, duration, filename)
}

func writeM3U8Footer(w io.Writer) {
	fmt.Fprintln(w, `#EXT-X-ENDLIST`)
}

func testInputGob(pathGob string, pathOut string, testSeg bool, writeM3u8 bool) {
	var m3u8file *os.File
	lastFilename := pathOut

	gobfile, _ := os.Open(pathGob)
	outfile, _ := os.Create(pathOut)
	dec := gob.NewDecoder(gobfile)
	var allSamples GobAllSamples
	dec.Decode(&allSamples)

	if writeM3u8 {
		m3u8file, _ = os.Create("index.m3u8")
		writeM3U8Header(m3u8file)
	}

	muxer := &ts.Muxer{
		W: outfile,
	}
	trackH264 := muxer.AddH264Track()
	trackH264.SPS = allSamples.SPS
	trackH264.PPS = allSamples.PPS
	trackH264.SetTimeScale(int64(allSamples.TimeScale))
	muxer.WriteHeader()

	lastPTS := int64(0)
	syncCount := 0
	segCount := 0

	appendM3u8Item := func() {
		info, _ := outfile.Stat()
		size := info.Size()
		dur := float64(trackH264.PTS - lastPTS) / float64(allSamples.TimeScale)
		writeM3U8Item(m3u8file, lastFilename, size, dur)
	}

	for i, sample := range allSamples.Samples {
		if debugStream {
			fmt.Println("stream: write sample #", i)
		}
		if sample.Sync {
			syncCount++
			if testSeg {
				if syncCount % 2 == 0 {
					filename := fmt.Sprintf("%s.seg%d.ts", pathOut, segCount)

					if debugStream {
						fmt.Println("stream:", "seg", segCount, "sync", syncCount, trackH264.PTS)
					}

					if m3u8file != nil {
						appendM3u8Item()
					}

					lastFilename = filename
					outfile.Close()
					segCount++
					outfile, _ = os.Create(filename)
					muxer.W = outfile
					muxer.WriteHeader()
					lastPTS = trackH264.PTS
				}
			}
		}

		trackH264.WriteH264NALU(sample.Sync, sample.Duration, sample.Data)
	}

	if m3u8file != nil {
		appendM3u8Item()
		writeM3U8Footer(m3u8file)
		m3u8file.Close()
	}

	outfile.Close()
	if debugStream {
		fmt.Println("stream: written to", pathOut)
	}
}

func doFilterFile(infile string, outfile string) {
	var inf, outf *os.File
	var err error

	if inf, err = os.Open(infile); err != nil {
		return
	}
	if outf, err = os.Create(outfile); err != nil {
		return
	}

	packet := make([]byte, 188)
	patNr := 0
	pmtNr := 0

	for {
		if _, err = inf.Read(packet); err != nil {
			break
		}
		br := bytes.NewReader(packet)
		header, _ := ts.ReadTSHeader(br)
		skip := false

		if header.PID == 17 {
			skip = true
		}

		if header.PID == 4096 {
			if pmtNr > 0 {
				skip = true
			}
			pmtNr++
		}

		if header.PID == 0 {
			if patNr > 0 {
				skip = true
			}
			patNr++
		}

		if !skip {
			outf.Write(packet)
		}
	}

	inf.Close()
	outf.Close()
}

func makeDiscontAudioMp4() {
	infilev, _ := os.Open("movie.mp4")
	mrv := mp4.Demuxer{R: infilev}
	mrv.ReadHeader()
	trackv := mrv.TrackH264

	infilea, _ := os.Open("pray.mp4")
	mra := mp4.Demuxer{R: infilea}
	mra.ReadHeader()
	tracka := mra.TrackAAC

	outfile, _ := os.Create("pray.discont.mp4")
	mw := mp4.Muxer{W: outfile}
	mw.WriteHeader()

	trackwv := mw.AddH264Track()
	trackwv.SetH264PPSAndSPS(trackv.GetH264PPSAndSPS())
	trackwv.SetTimeScale(trackv.TimeScale())

	trackwa := mw.AddAACTrack()
	trackwa.SetMPEG4AudioConfig(tracka.GetMPEG4AudioConfig())
	trackwa.SetTimeScale(tracka.TimeScale())

	n := 0
	time := float64(0.0)
	for {
		pts, dts, isKeyFrame, data, err := trackv.ReadSample()
		if isKeyFrame {
			n++
		}
		time = trackv.TsToTime(dts)
		if err != nil || n >= 2 {
			break
		}
		trackwv.WriteSample(pts, dts, isKeyFrame, data)
	}

	hole := 0
	delta := int64(0)
	for {
		pts, dts, isKeyFrame, data, err := tracka.ReadSample()
		if err != nil || tracka.TsToTime(dts) > time {
			break
		}
		if tracka.CurTime() > 1.0 && hole == 0 {
			delta = tracka.TimeToTs(1.0)
			hole++
		}
		trackwa.WriteSample(pts+delta, dts+delta, isKeyFrame, data)
	}

	mw.WriteTrailer()
	outfile.Close()
}

func readTsAudioWriteMp4() {
	infile, _ := os.Open("pray.ts")
	tr := &ts.Demuxer{R: infile}
	tr.ReadHeader()

	outfile, _ := os.Create("pray.ts.mp4")
	mw := &mp4.Muxer{W: outfile}
	trackw := mw.AddAACTrack()
	mw.WriteHeader()

	config := tr.TrackAAC.GetMPEG4AudioConfig()
	trackw.SetMPEG4AudioConfig(config)
	trackw.SetTimeScale(tr.TimeScale())

	for {
		pts, dts, _, data, err := tr.TrackAAC.ReadSample()
		if err != nil {
			break
		}
		trackw.WriteSample(pts, dts, false, data)
	}

	mw.WriteTrailer()
	outfile.Close()
}

// h264 payload type
// annex-b: [00 00 00 01] + NALU
// avcc:   [4 byte length of NALU] + NALU
// raw

// aac payload type
// adts
// raw

func readMp4VideoWriteTs() {
	infile1, _ := os.Open("movie.mp4")
	mr1 := mp4.Demuxer{R: infile1}
	mr1.ReadHeader()
	trackv := mr1.TrackH264

	outfile, _ := os.Create("movie.out.ts")
	mw := &ts.Muxer{W: outfile}
	trackwv := mw.AddH264Track()
	trackwv.SetTimeScale(trackv.TimeScale())
	trackwv.SetH264PPSAndSPS(trackv.GetH264PPSAndSPS())
	mw.WriteHeader()

	n := 0
	for {
		pts, dts, isKeyFrame, data, err := trackv.ReadSample()
		if isKeyFrame {
			n++
		}
		if n == 2 {
			break
		}
		if err != nil {
			break
		}
		trackwv.WriteSample(pts, dts, isKeyFrame, data)
	}

	outfile.Close()
}

func readMp4AudioWriteTs() {
	infile1, _ := os.Open("pray.mp4")
	mr1 := mp4.Demuxer{R: infile1}
	mr1.ReadHeader()
	tracka := mr1.TrackAAC

	outfile, _ := os.Create("pray.out.ts")
	mw := &ts.Muxer{W: outfile}
	trackwa := mw.AddAACTrack()
	trackwa.SetTimeScale(tracka.TimeScale())
	trackwa.SetMPEG4AudioConfig(tracka.GetMPEG4AudioConfig())
	mw.WriteHeader()

	for i := 0; i < 300; i++ {
		pts, dts, isKeyFrame, data, err := tracka.ReadSample()
		if err != nil {
			break
		}
		trackwa.WriteSample(pts, dts, isKeyFrame, data)
	}

	outfile.Close()
}

func readMp4AudioVideoWriteTs() {
	infile1, _ := os.Open("pray.mp4")
	mr1 := mp4.Demuxer{R: infile1}
	mr1.ReadHeader()
	tracka := mr1.TrackAAC

	infile2, _ := os.Open("movie.mp4")
	mr2 := mp4.Demuxer{R: infile2}
	mr2.ReadHeader()
	trackv := mr2.TrackH264

	outfile, _ := os.Create("mv.out.ts")
	mw := &ts.Muxer{W: outfile}
	trackwa := mw.AddAACTrack()
	trackwa.SetTimeScale(tracka.TimeScale())
	trackwa.SetMPEG4AudioConfig(tracka.GetMPEG4AudioConfig())
	trackwv := mw.AddH264Track()
	trackwv.SetTimeScale(trackv.TimeScale())
	trackwv.SetH264PPSAndSPS(trackv.GetH264PPSAndSPS())
	mw.WriteHeader()

	n := 0
	for {
		pts, dts, isKeyFrame, data, err := trackv.ReadSample()
		if isKeyFrame {
			n++
		}
		if err != nil || n == 2 {
			break
		}
		trackwv.WriteSample(pts, dts, isKeyFrame, data)

		for {
			pts, dts, _, data, err := tracka.ReadSample()
			if err != nil {
				break
			}
			trackwa.WriteSample(pts, dts, false, data)
			if tracka.CurTime() > trackv.CurTime() {
				break
			}
		}
	}

	outfile.Close()
}

// 

func CreateTestdata() {
/*
	infile1, _ := os.Open("movie.mp4")
	mr1 := mp4.Demuxer{R: infile1}
	mr1.ReadHeader()
	trackv := mr1.TrackH264

	infile2, _ := os.Open("pray.mp4")
	mr2 := mp4.Demuxer{R: infile2}
	mr2.ReadHeader()
	tracka := mr2.TrackAAC

	var all data.All
	aac := &all.AAC
	h264 := &all.H264

	aac.TimeScale = tracka.TimeScale()
	h264.TimeScale = trackv.TimeScale()

	gop := 0
	time := float64(0)
	for {
		pts, dts, isKeyFrame, frame, err := trackv.ReadSample()
		if isKeyFrame {
			gop++
		}
		if gop == 2 {
			break
		}
		if err != nil {
			break
		}
		time = trackv.CurTime()
		h264.Packets = append(h264.Packets, data.Packet{pts,dts,isKeyFrame,frame})
	}

	for {
		pts, dts, isKeyFrame, frame, err := tracka.ReadSample()
		if err != nil || tracka.CurTime() > time {
			break
		}
		aac.Packets = append(aac.Packets, data.Packet{pts,dts,isKeyFrame,frame})
	}

	dumpfile, _ := os.Create("../avtest-data/data.go")
	fmt.Fprintln(dumpfile, "package data")
	fmt.Fprint(dumpfile, "var Streams =")
	dump.Write(dumpfile, all)
	dumpfile.Close()
*/
}

func dumpTsFileInfo() {
	// demuxer
	// muxer
}

func rewriteMp4Audio() {
	infile, _ := os.Open("pray.mp4")
	mr := mp4.Demuxer{R: infile}
	mr.ReadHeader()
	track := mr.TrackAAC

	outfile, _ := os.Create("pray2.mp4")
	mw := mp4.Muxer{W: outfile}
	mw.WriteHeader()
	trackw := mw.AddAACTrack()

	config := track.GetMPEG4AudioConfig()
	trackw.SetMPEG4AudioConfig(config)
	trackw.SetTimeScale(track.TimeScale())

	for i := 0; i < 500; i++ {
		pts, dts, _, data, err := track.ReadSample()
		if err != nil {
			break
		}
		if i <= 200 {
			trackw.WriteSample(pts, dts, false, data)
		}
	}

	mw.WriteTrailer()
	outfile.Close()
}

func rewriteMp4Video() {
	infile, _ := os.Open("movie.mp4")
	mr := mp4.Demuxer{R: infile}
	mr.ReadHeader()
	track := mr.TrackH264

	outfile, _ := os.Create("movie2.mp4")
	mw := mp4.Muxer{W: outfile}
	mw.WriteHeader()
	trackw := mw.AddH264Track()

	trackw.SetH264PPSAndSPS(track.GetH264PPSAndSPS())
	trackw.SetTimeScale(track.TimeScale())

	for i := 0; i < 500; i++ {
		pts, dts, isKeyFrame, data, err := track.ReadSample()
		if err != nil {
			break
		}
		if i <= 200 {
			trackw.WriteSample(pts, dts, isKeyFrame, data)
		}
	}

	mw.WriteTrailer()
	outfile.Close()
}

func mixMp4VideoAndAudio() {
	infilev, _ := os.Open("movie.mp4")
	mrv := mp4.Demuxer{R: infilev}
	mrv.ReadHeader()
	trackv := mrv.TrackH264

	infilea, _ := os.Open("pray.mp4")
	mra := mp4.Demuxer{R: infilea}
	mra.ReadHeader()
	tracka := mra.TrackAAC

	outfile, _ := os.Create("mv2.mp4")
	mw := mp4.Muxer{W: outfile}
	mw.WriteHeader()

	trackwv := mw.AddH264Track()
	trackwv.SetH264PPSAndSPS(trackv.GetH264PPSAndSPS())
	trackwv.SetTimeScale(trackv.TimeScale())

	trackwa := mw.AddAACTrack()
	trackwa.SetMPEG4AudioConfig(tracka.GetMPEG4AudioConfig())
	trackwa.SetTimeScale(tracka.TimeScale())

	n := 0
	time := float64(0.0)
	for {
		pts, dts, isKeyFrame, data, err := trackv.ReadSample()
		if isKeyFrame {
			n++
		}
		time = trackv.TsToTime(dts)
		if err != nil || n >= 2 {
			break
		}
		trackwv.WriteSample(pts, dts, isKeyFrame, data)
	}

	for {
		pts, dts, isKeyFrame, data, err := tracka.ReadSample()
		if err != nil || tracka.TsToTime(dts) > time {
			break
		}
		trackwa.WriteSample(pts, dts, isKeyFrame, data)
	}

	mw.WriteTrailer()
	outfile.Close()
}

func dumpMp4(filename string) {
	file, _ := os.Open(filename)
	mr := mp4.Demuxer{R: file}
	mr.ReadHeader()
	log, _ := os.Create(filename+".log")
	dumper := &atom.Dumper{W: log}
	atom.WalkMovie(dumper, mr.MovieAtom)
}

func dumpMp4FileInfo() {
	dumpMp4("pray.mp4")
	dumpMp4("pray2.mp4")
	dumpMp4("movie.mp4")
	dumpMp4("movie2.mp4")
	dumpMp4("mv.mp4")
	dumpMp4("mv2.mp4")
	dumpMp4("pray.ts.mp4")
	dumpMp4("pray.ts.avconv.mp4")
	dumpMp4("pray.discont.mp4")
}

func dumpFragMp4(filename string) {
	file, _ := os.Open(filename)
	dumpfile, _ := os.Create(filename+".fragdump.log")
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
			fmt.Println(cc4)
			frag, _ := atom.ReadMovieFrag(rd)
			if frag.Tracks[0].Header.Id < 3 {
				atom.WalkMovieFrag(dumper, frag)
			}
		} else if cc4 == "moov" {
			moov, _ := atom.ReadMovie(rd)
			atom.WalkMovie(dumper, moov)
		} else {
			io.CopyN(ioutil.Discard, rd, rd.N)
			if cc4 == "moov" {
				initSegEnd, _ = file.Seek(0, 1)
				fmt.Println("initSegEnd", initSegEnd)
			}
			if cc4 == "mdat" {
				posEnd, _ = file.Seek(0, 1)
				output.Entries = append(output.Entries, Entry{posStart,posEnd})
				fmt.Println("fragRange", posStart, posEnd)
			}
		}
	}

	output.InitSegEnd = initSegEnd
	outfile, _ := os.Create(filename+".fraginfo.json")
	json.NewEncoder(outfile).Encode(output)
	outfile.Close()
}

func main() {
	input := flag.String("i", "", "input file")
	output := flag.String("o", "", "output file")
	inputGob := flag.String("g", "", "input gob file")
	testSegment := flag.Bool("seg", false, "test segment")
	writeM3u8 := flag.Bool("m3u8", false, "write m3u8 file")
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to file")
	filter := flag.Bool("filter", false, "test filter file")
	test := flag.Bool("test", false, "test")
	gentest := flag.Bool("gentest", false, "gentest")
	probe := flag.String("probe", "", "probe mp4 file")
	dumpfrag := flag.String("dumpfrag", "", "dump fragmented mp4 file")

	flag.BoolVar(&debugData, "vd", false, "debug data")
	flag.BoolVar(&debugStream, "vs", false, "debug stream")
	flag.BoolVar(&debugHeader, "vh", false, "debug header")
	flag.BoolVar(&ts.DebugReader, "vr", false, "debug reader")
	flag.BoolVar(&ts.DebugWriter, "vw", false, "debug writer")
	flag.Parse()

	if *probe != "" {
		dumpMp4(*probe)
		return
	}

	if *dumpfrag != "" {
		dumpFragMp4(*dumpfrag)
		return
	}

	if *gentest {
		CreateTestdata()
		return
	}

	if *test {
		readTsAudioWriteMp4()
		rewriteMp4Video()
		rewriteMp4Audio()
		mixMp4VideoAndAudio()
		makeDiscontAudioMp4()
		dumpMp4FileInfo()
		readMp4AudioVideoWriteTs()
		//readMp4AudioWriteTs()
		//readMp4VideoWriteTs()
		return
	}

	if *filter && *input != "" && *output != "" {
		doFilterFile(*input, *output)
		return
	}

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			return
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	if *inputGob != "" && *output != "" {
		testInputGob(*inputGob, *output, *testSegment, *writeM3u8)
		return
	}

	var file *os.File
	var err error

	ch := make(chan Sample, 0)
	go readSamples(*input, ch)

	if *output != "" {
		if file, err = os.Create(*output); err != nil {
			return
		}
	}

	writePAT := func() (err error) {
		pat := ts.PAT{
			Entries: []ts.PATEntry{
				{ProgramNumber: 1, ProgramMapPID: 0x1000},
			},
		}
		if err = ts.WritePATPacket(file, pat); err != nil {
			return
		}
		return
	}

	writePMT := func() (err error) {
		pmt := ts.PMT{
			PCRPID: 0x100,
			ElementaryStreamInfos: []ts.ElementaryStreamInfo{
				{StreamType: ts.ElementaryStreamTypeH264, ElementaryPID: 0x100},
				{StreamType: ts.ElementaryStreamTypeAdtsAAC, ElementaryPID: 0x101},
			},
		}
		if err = ts.WritePMTPacket(file, pmt, 0x1000); err != nil {
			return
		}
		return
	}

	var tswH264, tswAAC *ts.TSWriter
	var sample Sample

	writeSample := func() (err error) {
		var w *ts.TSWriter
		var streamId uint

		switch sample.Type {
		case ts.ElementaryStreamTypeH264:
			streamId = ts.StreamIdH264
			w = tswH264
		case ts.ElementaryStreamTypeAdtsAAC:
			streamId = ts.StreamIdAAC
			w = tswAAC
		}

		pes := ts.PESHeader{
			StreamId: streamId,
			PTS: sample.PTS,
			DTS: sample.DTS,
		}
		w.PCR = sample.PCR
		//w.DisableHeaderPadding = true

		if sample.Type == ts.ElementaryStreamTypeAdtsAAC {
			w.RandomAccessIndicator = true
		} else {
			w.RandomAccessIndicator = sample.RandomAccessIndicator
		}
		if err = ts.WritePESPacket(w, pes, sample.Data); err != nil {
			return
		}
		return
	}

	if file != nil {
		writePAT()
		writePMT()
		tswH264 = &ts.TSWriter{
			W: file,
			PID: 0x100,
		}
		tswAAC = &ts.TSWriter{
			W: file,
			PID: 0x101,
		}
	}

	for {
		var ok bool
		if sample, ok = <-ch; !ok {
			break
		}

		if debugStream {
			fmt.Println("sample: ", sample.Type, len(sample.Data),
				"PCR", sample.PCR, "PTS", sample.PTS,
				"DTS", sample.DTS, "sync", sample.RandomAccessIndicator,
			)
			//fmt.Print(hex.Dump(sample.Data))
		}

		if file != nil {
			writeSample()
		}
	}
}

// 
// Design of avtest
// demux and compare
// mux and test play
//

/*
func CompareStream(a, b data.Stream) bool {
	if a.TimeScale != b.TimeScale {
		return false
	}
	if len(a.Packets) != len(b.Packets) {
		return false
	}
	for i := range(a.Packets) {
		pa := a.Packets[i]
		pb := b.Packets[i]
		if pa.Pts != pb.Pts || pa.Dts != pb.Dts || pa.IsKeyFrame != pb.IsKeyFrame {
			return false
		}
		if len(pa.Data) != len(pb.Data) {
			return false
		}
	}
	return true
}
*/

func testMp4Mux() {
}

func TestMuxerAndDemuxers() {
	/*
	newMp4Muxer := func(file *os.File) *Muxer {
		return &Muxer{mp4: &mp4.Muxer{W: file}}
	}
	newTsMuxer := func(file *os.File) *Muxer {
		return &Muxer{ts: &ts.Muxer{W: file}}
	}
	newMp4Demuxer := func(file *os.File) *Demuxer {
		return &Demuxer{mp4: &mp4.Demuxer{R: file}}
	}
	newTsDemuxer := func(file *os.File) *Demuxer {
		return &Demuxer{ts: &ts.Demuxer{R: file}}
	}

	fileidx := 0
	testMuxer := func(ext string, createMuxer func(*os.File)*Muxer, stream data.Stream) {
		file, _ := os.Create(fmt.Sprintf("%d.%s", fileidx, ext))
		muxer := createMuxer(file)
		trackv := muxer.AddH264Track()
		trackv.SetH264PPSAndSPS(stream.H264.SPS, stream.H264.PPS)
		tracka := muxer.AddAACTrack()
		tracka.SetMPEG4AudioConfig(stream.AAC.MPEG4AudioConfig)
	}
	*/
}

type Demuxer struct {
	mp4 *mp4.Demuxer
	ts *ts.Demuxer
}

func (self *Demuxer) ReadHeader() error {
	if self.mp4 != nil {
		return self.mp4.ReadHeader()
	}
	if self.ts != nil {
		return self.ts.ReadHeader()
	}
	return nil
}

func (self *Demuxer) Tracks() (tracks []*Track) {
	if self.mp4 != nil {
		for _, track := range(self.mp4.Tracks) {
			tracks = append(tracks, &Track{mp4: track})
		}
	}
	if self.ts != nil {
		for _, track := range(self.ts.Tracks) {
			tracks = append(tracks, &Track{ts: track})
		}
	}
	return
}

type Muxer struct {
	mp4 *mp4.Muxer
	ts *ts.Muxer
}

func (self *Muxer) Tracks() (tracks []*Track) {
	if self.mp4 != nil {
		for _, track := range(self.mp4.Tracks) {
			tracks = append(tracks, &Track{mp4: track})
		}
	}
	if self.ts != nil {
		for _, track := range(self.ts.Tracks) {
			tracks = append(tracks, &Track{ts: track})
		}
	}
	return
}

func (self *Muxer) AddH264Track() *Track {
	if self.mp4 != nil {
		return &Track{mp4: self.mp4.AddH264Track()}
	}
	if self.ts != nil {
		return &Track{ts: self.ts.AddH264Track()}
	}
	return nil
}

func (self *Muxer) AddAACTrack() *Track {
	if self.mp4 != nil {
		return &Track{mp4: self.mp4.AddAACTrack()}
	}
	if self.ts != nil {
		return &Track{ts: self.ts.AddAACTrack()}
	}
	return nil
}

func (self *Muxer) WriteHeader() error {
	if self.mp4 != nil {
		return self.mp4.WriteHeader()
	}
	if self.ts != nil {
		return self.ts.WriteHeader()
	}
	return nil
}

func (self *Muxer) WriteTrailer() error {
	if self.mp4 != nil {
		return self.mp4.WriteTrailer()
	}
	return nil
}

type Track struct {
	mp4 *mp4.Track
	ts *ts.Track
}

func (self *Track) Type() int {
	if self.mp4 != nil {
		return self.mp4.Type
	}
	if self.ts != nil {
		return self.ts.Type
	}
	return 0
}

func (self *Track) ReadSample() (pts int64, dts int64, isKeyFrame bool, data []byte, err error) {
	if self.mp4 != nil {
		return self.mp4.ReadSample()
	}
	if self.ts != nil {
		return self.mp4.ReadSample()
	}
	return
}

func (self *Track) WriteSample(pts int64, dts int64, isKeyFrame bool, data []byte) (err error) {
	if self.mp4 != nil {
		return self.mp4.WriteSample(pts,dts,isKeyFrame,data)
	}
	if self.ts != nil {
		return self.ts.WriteSample(pts,dts,isKeyFrame,data)
	}
	return
}

/*

av.CodecType
H264
AAC
G726
IsAudio()
IsVideo()

av.Stream
CodecData() data, ok
SetCodecData(data) err
Type() H264/AAC/G726
SetType()
String()
TimeScale()
SetTimeScale()
TsToTime()
TimeToTs()
//FillParamsByStream()

codec/h264parser
codec/aacparser
codec/ffmpeg/h264enc
codec/ffmpeg/h264dec

av.Muxer
NewStream() av.Stream
WriteHeader() error
WriteTrailer() error
WritePacket(av.Packet{}) error
ClearStreams()*

av.Demuxer
ReadHeader() error
ReadPacket() av.Packet
SeekToTime(1.11)

av.Packet
StreamIdx
Data
IsKeyFrame
Pts/Dts

rtsp.Demuxer
ReadHeader()
Streams()
ReadPacket() av.Packet

rtsp.Client
Describe()

testsuite contains 

*/

