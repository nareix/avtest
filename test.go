
package main

import (
	_ "bytes"
	"os"
	"io"
	"io/ioutil"
	"encoding/json"
	"github.com/nareix/mp4"
	"github.com/nareix/mp4/atom"
	"fmt"
	"flag"
)

func testMp4Demux() {
	file, _ := os.Open("projectindex.mp4")
	demuxer := &mp4.Demuxer{R: file}
	demuxer.ReadHeader()
	streams := demuxer.Streams()
	fmt.Println(streams)
	fmt.Println(demuxer.SeekToTime(80.0))
	for {
		pkt, err := demuxer.ReadPacket()
		if err != nil {
			break
		}
		fmt.Println(pkt.StreamIdx, streams[pkt.StreamIdx].TsToTime(pkt.Dts), len(pkt.Data))
	}
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

func main() {
	dumpfrag := flag.String("dumpfrag", "", "dump fragment mp4 info")
	flag.Parse()

	if *dumpfrag != "" {
		dumpFragMp4(*dumpfrag)
		return
	}
}

