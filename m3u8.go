
package main

import (
	"io"
	"fmt"
)

func writeM3U8Header(w io.Writer, targetDuration float64) {
	fmt.Fprintf(w, `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:%d
#EXT-X-MEDIA-SEQUENCE:374
`, int64(targetDuration))
}

func writeM3U8Item(w io.Writer, filename string, duration float64) {
	fmt.Fprintf(w, `#EXTINF:%.8f,
%s
`, duration, filename)
}

func writeM3U8Footer(w io.Writer) {
	fmt.Fprintln(w, `#EXT-X-ENDLIST`)
}

