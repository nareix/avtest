
package main

import (
	_ "bytes"
	"os"
	_ "io"
	_ "io/ioutil"
	"github.com/nareix/mp4"
	"fmt"
	_ "flag"
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

func testMp4Mux() {
}

func main() {
}

