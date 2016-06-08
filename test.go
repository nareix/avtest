
package main

import (
	"bytes"
	"os"
	"io"
	"math"
	"io/ioutil"
	"encoding/json"
	"encoding/binary"
	"github.com/nareix/av"
	"github.com/nareix/rtsp"
	"github.com/nareix/ts"
	"github.com/nareix/ffmpeg"
	"github.com/nareix/codec/aacparser"
	"github.com/nareix/mp4/atom"
	"github.com/nareix/mp4"
	"github.com/nareix/av/transcode"
	"fmt"
	"flag"
	"net/http"
)

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
	demuxer, _ := ts.Open(file)

	streams := demuxer.Streams()
	fmt.Fprintln(dumpfile, streams, err)
	aacTotalDur := float64(0)

	for {
		i, pkt, err := demuxer.ReadPacket()
		if err != nil {
			break
		}
		codec := streams[i]
		if codec.Type() == av.AAC {
			acodec := codec.(av.AudioCodecData)
			if _, _, samples, err := aacparser.ExtractADTSFrames(pkt.Data); err != nil {
				panic(err)
			} else {
				dur := float64(samples)/float64(acodec.SampleRate())
				aacTotalDur += dur
			}
		}
		fmt.Fprintln(dumpfile, codec.Type(), fmt.Sprintf("ts=%.2f dur=%.3f cts=%.3f", demuxer.CurrentTime(), pkt.Duration, pkt.CompositionTime),
			pkt.IsKeyFrame, len(pkt.Data), fmt.Sprintf("%x", pkt.Data[:4]))
	}
	fmt.Fprintln(dumpfile, "aacTotalDur", aacTotalDur)

	dumpfile.Close()
}

func testRtsp(uri string) (err error) {
	ffmpeg.SetLogLevel(ffmpeg.DEBUG)

	cli, err := rtsp.Open(uri)
	if err != nil {
		return
	}
	//cli.DebugConn = true

	findcodec := func(codec av.AudioCodecData) (ok bool, err error, dec av.AudioDecoder, enc av.AudioEncoder) {
		if codec.Type() == av.AAC {
			return
		}
		ok = true

		var ffdec *ffmpeg.AudioDecoder
		if ffdec, err = ffmpeg.NewAudioDecoder(codec); err != nil {
			return
		}
		fmt.Println("transcode", ffdec.SampleRate, ffdec.SampleFormat, ffdec.ChannelLayout, "> AAC")

		var ffenc *ffmpeg.AudioEncoder
		if ffenc, err = ffmpeg.NewAudioEncoderByName("aac"); err != nil {
			return
		}
		ffenc.SampleRate = 44100
		ffenc.ChannelLayout = av.CH_STEREO
		if err = ffenc.Setup(); err != nil {
			return
		}

		dec = ffdec
		enc = ffenc
		return
	}

	fmt.Println("connected")

	demuxer := &transcode.Demuxer{
		Transcoder: &transcode.Transcoder{
			FindAudioDecoderEncoder: findcodec,
		},
		Demuxer: cli,
	}
	if err = demuxer.Setup(); err != nil {
		return
	}
	streams := demuxer.Streams()
	for i, stream := range streams {
		fmt.Println("#",i, stream.IsVideo())
	}

	var outts *os.File
	if outts, err = os.Create("out.ts"); err != nil {
		return
	}
	var tsmux *ts.Muxer
	if tsmux, err = ts.Create(outts, streams); err != nil {
		return
	}

	var outmp4 *os.File
	if outmp4, err = os.Create("out.mp4"); err != nil {
		return
	}
	var mp4mux *mp4.Muxer
	if mp4mux, err = mp4.Create(outmp4, streams); err != nil {
		return
	}

	gop := 0
	durs := make([]float64, len(streams))

	for gop < 5 {
		var si int
		var pkt av.Packet
		si, pkt, err = demuxer.ReadPacket()
		if err != nil {
			return
		}
		durs[si] += pkt.Duration

		if streams[si].IsVideo() && pkt.IsKeyFrame {
			fmt.Println("gop:", gop)
			gop++
		}
		fmt.Println("#", si, "len", len(pkt.Data), "dur", fmt.Sprintf("%.3f", pkt.Duration), "avsync", durs[0]-durs[1])

		if gop > 0 {
			if err = tsmux.WritePacket(si, pkt); err != nil {
				return
			}
			if err = mp4mux.WritePacket(si, pkt); err != nil {
				return
			}
		}
	}

	if err = mp4mux.WriteTrailer(); err != nil {
		return
	}
	if err = tsmux.WriteTrailer(); err != nil {
		return
	}

	demuxer.Close()
	outts.Close()
	outmp4.Close()

	return
}

func testAACEnc(filename string) (err error) {
	var enc *ffmpeg.AudioEncoder
	if enc, err = ffmpeg.NewAudioEncoderByName("aac"); err != nil {
		return
	}

	enc.SampleFormat = av.FLTP
	enc.SampleRate = 8000
	enc.ChannelLayout = av.CH_MONO
	enc.BitRate = 50000
	if err = enc.Setup(); err != nil {
		return
	}

	codec := enc.CodecData().(aacparser.CodecData)
	time := float64(0)

	fillbuf := func(frame *av.AudioFrame) {
		channelCount := frame.ChannelLayout.Count()
		tincr := 2*math.Pi*440.0/float64(frame.SampleRate)

		if frame.SampleFormat.IsPlanar() {
			frame.Data = make([][]byte, channelCount)
			rawdata := []*bytes.Buffer{}
			for i := 0; i < channelCount; i++ {
				rawdata = append(rawdata, &bytes.Buffer{})
			}
			for i := 0; i < frame.SampleCount; i++ {
				val := float32(math.Sin(time))
				for j := 0; j < channelCount; j++ {
					binary.Write(rawdata[j], binary.LittleEndian, val)
				}
				time += tincr
			}
			for i := 0; i < channelCount; i++ {
				frame.Data[i] = rawdata[i].Bytes()
			}
		} else {
			frame.Data = make([][]byte, 1)
			rawdata := &bytes.Buffer{}
			for i := 0; i < frame.SampleCount; i++ {
				val := float32(math.Sin(time))
				for j := 0; j < channelCount; j++ {
					binary.Write(rawdata, binary.LittleEndian, val)
				}
				time += tincr
			}
			frame.Data[0] = rawdata.Bytes()
		}
	}

	genbuf := func(style int) (frame av.AudioFrame) {
		fmt.Println("genbuf", style)
		rates := []int{44100, 22000, 48000}
		formats := []av.SampleFormat{av.FLTP, av.FLT}
		layouts := []av.ChannelLayout{av.CH_MONO, av.CH_STEREO, av.CH_2_1}
		frame.SampleRate = rates[style%len(rates)]
		frame.ChannelLayout = layouts[style%len(layouts)]
		frame.SampleFormat = formats[style%len(formats)]
		frame.SampleCount = enc.FrameSampleCount
		fillbuf(&frame)
		return
	}

	file, _ := os.Create(filename)
	for i := 0; i < codec.SampleRate()*10/enc.FrameSampleCount; i++ {
		frame := genbuf(i)
		var pkts []av.Packet
		if pkts, err = enc.Encode(frame); err != nil {
			return
		}
		for _, pkt := range pkts {
			adtshdr := codec.MakeADTSHeader(enc.FrameSampleCount, len(pkt.Data))
			file.Write(adtshdr)
			file.Write(pkt.Data)
			fmt.Println("pkt", len(pkt.Data))
		}
	}
	file.Close()

	return
}

func testTranscode() (err error) {
	return
}

func main() {
	dumpts := flag.Bool("dumpts", false, "dump ts file info")
	dumpfrag := flag.String("dumpfrag", "", "dump fragment mp4 info")
	httpserver := flag.String("httpserver", "", "server http")

	testrtsp := flag.String("testrtsp", "", "test rtsp")
	testaacenc := flag.String("testaacenc", "", "test aac encoder")
	testtranscode := flag.Bool("testtranscode", false, "test transcode")

	flag.Parse()

	if *testaacenc != "" {
		if err := testAACEnc(*testaacenc); err != nil {
			panic(err)
		}
	}

	if *testrtsp != "" {
		if err := testRtsp(*testrtsp); err != nil {
			panic(err)
		}
	}

	if *testtranscode {
		if err := testTranscode(); err != nil {
			panic(err)
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

	if *httpserver != "" {
		http.ListenAndServe(*httpserver, http.FileServer(http.Dir(".")))
	}
}

