
package main

import (
	_ "bytes"
	"os"
	"io"
	"io/ioutil"
	"encoding/json"
	"github.com/nareix/av"
	"github.com/nareix/mp4"
	"github.com/nareix/ts"
	"github.com/nareix/mp4/atom"
	"fmt"
	"flag"
	"net/http"
)

func testMp4Demux() {
	var err error
	outfile, _ := os.Create("out.mp4")
	defer outfile.Close()

	outfile2, _ := os.Create("out.ts")
	defer outfile2.Close()

	//infile, _ := os.Open("projectindex.mp4")
	//demuxer := &mp4.Demuxer{R: infile}
	demuxer := &FFDemuxer{Filename: "projectindex.mp4"}

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
	fmt.Println(demuxer.SeekToTime(14.3*60.0))

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
		file, _ := os.Create(filename)
		muxer := &ts.Muxer{W: file}
		setStreams(muxer)
		muxer.WriteHeader()
		for _, pkt := range pkts {
			muxer.WritePacket(pkt.i, pkt.Packet)
		}
		file.Close()
		writeM3U8Item(indexfile, filename, totalTime)
	}

	writeM3U8Footer(indexfile)
	indexfile.Close()
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
	fmt.Println(streams, err)

	for {
		i, pkt, err := demuxer.ReadPacket()
		if err != nil {
			break
		}

		fmt.Println(streams[i].Type(), fmt.Sprintf("ts=%.2f dur=%.3f cts=%.3f", demuxer.Time(), pkt.Duration, pkt.CompositionTime),
			pkt.IsKeyFrame, len(pkt.Data), fmt.Sprintf("%x", pkt.Data[:4]))
	}
	dumpfile.Close()
}

func main() {
	dumpts := flag.String("dumpts", "", "dump ts file info")
	dumpfrag := flag.String("dumpfrag", "", "dump fragment mp4 info")
	httpserver := flag.String("httpserver", "", "server http")
	test := flag.Bool("test", false, "test")
	flag.Parse()

	if *dumpts != "" {
		dumpTs(*dumpts)
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

