
package main

import (
	"bytes"
	"os"
	"io"
	"math"
	"time"
	"image/png"
	"io/ioutil"
	"encoding/json"
	"encoding/binary"
	_"encoding/hex"
	"github.com/nareix/joy4/av"
	"github.com/nareix/joy4/format"
	"github.com/nareix/joy4/av/avutil"
	"github.com/nareix/joy4/format/rtsp"
	"github.com/nareix/joy4/cgo/ffmpeg"
	"github.com/nareix/joy4/codec/aacparser"
	"github.com/nareix/joy4/format/mp4/atom"
	"github.com/nareix/joy4/av/transcode"
	"github.com/nareix/joy4/format/rtmp"
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

func rtspDumpPCMU(cli *rtsp.Client) (err error) {
	outfile, _ := os.Create("out.mulaw")

	streams, _ := cli.Streams()
	acodec := streams[1].(av.AudioCodecData)

	var ffdec *ffmpeg.AudioDecoder
	if ffdec, err = ffmpeg.NewAudioDecoder(acodec); err != nil {
		return
	}

	for {
		var pkt av.Packet
		if pkt, err = cli.ReadPacket(); err != nil {
			return
		}
		if pkt.Idx == 1 {
			var frame av.AudioFrame
			var got bool
			if got, frame, err = ffdec.Decode(pkt.Data); err != nil {
				return
			}
			fmt.Println("pcmu", pkt.Time, len(pkt.Data), got, frame.SampleCount)

			outfile.Write(pkt.Data)
		}
	}

	return
}

func testRtsp(uri string) (err error) {
	ffmpeg.SetLogLevel(ffmpeg.DEBUG)

	cli, err := rtsp.DialTimeout(uri, time.Second*10)
	if err != nil {
		return
	}
	cli.Headers = append(cli.Headers, "User-Agent: Lavf57.25.100")
	cli.RtpTimeout = time.Second*10
	cli.RtspTimeout = time.Second*10
	cli.DebugRtsp = true
	//cli.DebugRtp = true
	cli.SkipErrRtpBlock = true
	cli.RtpKeepAliveTimeout = time.Second*3
	fmt.Println("connected")

	var streams []av.CodecData
	if streams, err = cli.Streams(); err != nil {
		return
	}
	fmt.Println("probe done")

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

	var demuxer av.Demuxer
	transcoder := &transcode.Demuxer{
		Transcoder: &transcode.Transcoder{
			FindAudioDecoderEncoder: findcodec,
		},
		Demuxer: cli,
	}
	if err = transcoder.Setup(); err != nil {
		return
	}
	demuxer = transcoder

	streams, _ = demuxer.Streams()
	for i, stream := range streams {
		fmt.Println("#",i, stream.Type().IsVideo())
	}

	var tsmux av.MuxCloser
	if tsmux, err = avutil.Create("out.ts", streams); err != nil {
		return
	}
	defer tsmux.Close()

	var mp4mux av.MuxCloser
	if mp4mux, err = avutil.Create("out.mp4", streams); err != nil {
		return
	}
	defer mp4mux.Close()

	gop := 0

	for gop < 10 {
		var pkt av.Packet
		pkt, err = demuxer.ReadPacket()
		if err == rtsp.ErrCodecDataChange {
			if _, err = cli.HandleCodecDataChange(); err != nil {
				return
			}
			err = fmt.Errorf("codec data changed")
			return
		}
		if err != nil {
			return
		}

		if streams[pkt.Idx].Type().IsVideo() && pkt.IsKeyFrame {
			fmt.Println("gop:", gop)
			gop++
		}
		fmt.Println("#", pkt.Idx, streams[pkt.Idx].Type(), "len", len(pkt.Data), "time", pkt.Time)

		if gop > 0 {
			if err = tsmux.WritePacket(pkt); err != nil {
				return
			}
			if err = mp4mux.WritePacket(pkt); err != nil {
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

	transcoder.Close()

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
		var pkts [][]byte
		if pkts, err = enc.Encode(frame); err != nil {
			return
		}
		for _, pkt := range pkts {
			adtshdr := aacparser.MakeADTSHeader(codec.Config, enc.FrameSampleCount, len(pkt))
			file.Write(adtshdr)
			file.Write(pkt)
			fmt.Println("pkt", len(pkt))
		}
	}
	file.Close()

	return
}

func testH264Dec(filename string) (err error) {
	var file av.DemuxCloser
	if file, err = avutil.Open(filename); err != nil {
		return
	}

	var streams []av.CodecData
	if streams, err = file.Streams(); err != nil {
		return
	}

	h264 := -1
	for i, stream := range streams {
		if stream.Type() == av.H264 {
			h264 = i
		}
	}
	if h264 == -1 {
		err = fmt.Errorf("h264 stream not found")
		return
	}

	var dec *ffmpeg.VideoDecoder
	if dec, err = ffmpeg.NewVideoDecoder(streams[h264]); err != nil {
		return
	}

	imgidx := 0

	for {
		var pkt av.Packet
		if pkt, err = file.ReadPacket(); err != nil {
			return
		}

		if int(pkt.Idx) == h264 && pkt.IsKeyFrame {
			var img *ffmpeg.VideoFrame
			if img, err = dec.Decode(pkt.Data); err != nil {
				return
			}
			if img != nil {
				fmt.Println("h264: decoded", pkt.Time)
				pngfile, _ := os.Create(fmt.Sprintf("%s.%d.png", filename, imgidx))
				png.Encode(pngfile, &img.Image)
				pngfile.Close()
				imgidx++
				img.Free()
			}
		}
	}

	return
}

func testRtmpServer() (err error) {
	server := &rtmp.Server{}
	server.Debug = true
	server.DebugConn = false

	handlePlay := func(conn *rtmp.Conn) (err error) {
		var demuxer av.DemuxCloser

		uri := ""
		uri = "rtmp://live.hkstv.hk.lxdns.com/live/hks"
		//uri = "rtsp://95.131.181.226:9099/onvif/media/PRF00.wxp"
		//uri = "rtsp://admin:123456@176.99.65.80:558/mpeg4cif"
		uri = "bug.ts"
		//uri = "projectindex-0.flv"

		fmt.Println("play:", uri)
		if demuxer, err = avutil.Open(uri); err != nil {
			return
		}
		defer demuxer.Close()

		var streams []av.CodecData
		if streams, err = demuxer.Streams(); err != nil {
			return
		}
		fmt.Println("streams:")
		for _, stream := range streams {
			fmt.Println(stream.Type())
		}

		if err = conn.WriteHeader(streams); err != nil {
			return
		}
		//conn.Debug = true

		gop := 0

		var pkt av.Packet
		for i := 0; ; i++ {
			pkt, err = demuxer.ReadPacket()
			if err != nil {
				err = nil
				break
			}

			if pkt.IsKeyFrame {
				gop++
			}

			if gop > 0 {
				fmt.Println("write", pkt.Idx, pkt.Time, len(pkt.Data), pkt.IsKeyFrame)
				if err = conn.WritePacket(pkt); err != nil {
					return
				}
			}
		}

		select {}

		return
	}

	handlePublish := func(conn *rtmp.Conn) (err error) {
		var streams []av.CodecData

		fmt.Println("publish:", conn.Path)
		if streams, err = conn.Streams(); err != nil {
			return
		}

		fmt.Println("publish: streams:")
		for _, stream := range streams {
			fmt.Println(stream.Type())
		}

		conn.Debug = false
		var pkt av.Packet
		for i := 0; i < 10; i++ {
			if pkt, err = conn.ReadPacket(); err != nil {
				return
			}
			fmt.Println(streams[pkt.Idx].Type(), pkt.Time, len(pkt.Data))
		}

		return
	}

	server.HandlePublish = func(conn *rtmp.Conn) {
		if err := handlePublish(conn); err != nil {
			fmt.Println(err)
		}
	}

	server.HandlePlay = func(conn *rtmp.Conn) {
		if err := handlePlay(conn); err != nil {
			fmt.Println(err)
		}
	}

	if err = server.ListenAndServe(); err != nil {
		return
	}

	return
}

func testNormailizer() (err error) {
	/*
	var lineb []byte
	br := bufio.NewReader(os.Stdin)

	streams := []av.CodecData{
		fakeCodec{typ: av.H264},
		fakeCodec{typ: av.AAC},
	}
	normalizer := pktque.NewNormalizer(streams)

	for {
		if lineb, _, err = br.ReadLine(); err != nil {
			return
		}
		line := string(lineb)
		a := strings.Split(line, " ")

		var idx int
		fmt.Sscanf(a[2], "%d", &idx)
		tm, _ := time.ParseDuration(a[3])
		pkt := av.Packet{Idx: int8(idx), Time: tm}

		fmt.Println("in", idx, tm)

		normalizer.Push(pkt)
		for {
			var ok bool
			if pkt, _, ok = normalizer.Pop(); !ok {
				break
			}
			fmt.Println("out", pkt.Idx, pkt.Time)
		}
	}
	*/
	return
}

func playurl(url string) (err error) {
	var demuxer av.DemuxCloser
	if demuxer, err = avutil.Open(url); err != nil {
		return
	}
	var streams []av.CodecData
	if streams, err = demuxer.Streams(); err != nil {
		return
	}
	for i, stream := range streams {
		fmt.Println(i, stream.Type())
	}

	for {
		var pkt av.Packet
		if pkt, err = demuxer.ReadPacket(); err != nil {
			return
		}
		fmt.Println(pkt.Idx, pkt.Time, pkt.IsKeyFrame, len(pkt.Data))
	}
}

func init() {
	format.RegisterAll()
}

func rtmpPublish(url string, filename string) (err error) {
	var conn *rtmp.Conn
	if conn, err = rtmp.Dial(url); err != nil {
		return
	}
	var file av.DemuxCloser
	if file, err = avutil.Open(filename); err != nil {
		return
	}
	var streams []av.CodecData
	if streams, err = file.Streams(); err != nil {
		return
	}
	if err = conn.WriteHeader(streams); err != nil {
		return
	}
	for {
		var pkt av.Packet
		if pkt, err = file.ReadPacket(); err != nil {
			break
		}
		fmt.Println("publish", pkt.Time, pkt.Idx, len(pkt.Data))
		if err = conn.WritePacket(pkt); err != nil {
			return
		}
		time.Sleep(time.Millisecond*10)
	}
	return
}

func main() {
	dumpfrag := flag.String("dumpfrag", "", "dump fragment mp4 info")
	httpserver := flag.String("httpserver", "", "server http")

	play := flag.String("play", "", "play url")
	testrtsp := flag.String("testrtsp", "", "test rtsp")
	testaacenc := flag.String("testaacenc", "", "test aac encoder")
	testh264dec := flag.String("testh264dec", "", "test h264 decoder")
	rtmpserver := flag.Bool("rtmpserver", false, "rtmp server")
	rtmppublish := flag.String("rtmppublish", "", "rtmp publish")

	flag.Parse()

	var err error

	if *play != "" {
		err = playurl(*play)
	}

	if *rtmpserver {
		err = testRtmpServer()
	}

	if *rtmppublish != "" {
		args := flag.Args()
		if len(args) > 0 {
			err = rtmpPublish(*rtmppublish, args[0])
		}
	}

	if *testaacenc != "" {
		err = testAACEnc(*testaacenc)
	}

	if *testh264dec != "" {
		err = testH264Dec(*testh264dec)
	}

	if *testrtsp != "" {
		err = testRtsp(*testrtsp)
	}

	if *dumpfrag != "" {
		dumpFragMp4(*dumpfrag)
	}

	if *httpserver != "" {
		err = http.ListenAndServe(*httpserver, http.FileServer(http.Dir(".")))
	}

	if err != nil {
		fmt.Println(err)
	}
}

