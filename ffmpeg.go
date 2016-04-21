
package main

/*
#cgo LDFLAGS: -lavformat -lavutil -lavcodec
#cgo CFLAGS: -Werror
#include <libavformat/avformat.h>

const int AAC = CODEC_ID_AAC;
const int H264 = CODEC_ID_H264;

typedef struct {
	AVFormatContext *fmt_ctx;
	AVPacket *pkt;
} ffctx_t;

void ffctx_openfile(ffctx_t *ctx, const char *filename, int *n) {
	av_register_all();
	avformat_open_input(&ctx->fmt_ctx, filename, NULL, NULL);
	//av_dump_format(ctx->fmt_ctx, 0, filename, 0);
	ctx->pkt = malloc(sizeof(AVPacket));
	*n = ctx->fmt_ctx->nb_streams;
}

void ffctx_close(ffctx_t *ctx) {
	free(ctx->pkt);
	avformat_close_input(&ctx->fmt_ctx);
}

void ffctx_get_stream_codec_extradata(ffctx_t *ctx, int i, void **p, int *len, int *p_id, int64_t *time_scale) {
	AVStream *stream = ctx->fmt_ctx->streams[i];
	*p = stream->codec->extradata;
	*len = stream->codec->extradata_size;
	*p_id = stream->codec->codec_id;
	*time_scale = stream->time_base.den;
}

void ffctx_read_frame(ffctx_t *ctx) {
	av_read_frame(ctx->fmt_ctx, ctx->pkt);
}

void ffctx_free_packet(ffctx_t *ctx) {
	av_free_packet(ctx->pkt);
}
*/
import "C"

import (
	"unsafe"
	"github.com/nareix/av"
)

type FFStream struct {
	av.StreamCommon
	timeScale int64
}

type FFDemuxer struct {
	Filename string
	time float64
	streams []*FFStream
	ctx *C.ffctx_t
}

func (self *FFDemuxer) Streams() (streams []av.Stream) {
	for _, stream := range(self.streams) {
		streams = append(streams, stream)
	}
	return
}

func (self *FFDemuxer) ReadHeader() (err error) {
	var streamNr C.int

	self.ctx = &C.ffctx_t{}
	C.ffctx_openfile(self.ctx, C.CString(self.Filename), &streamNr)

	for i := 0; i < int(streamNr); i++ {
		var p unsafe.Pointer
		var size, id C.int
		var timeScale C.int64_t
		stream := &FFStream{}
		C.ffctx_get_stream_codec_extradata(self.ctx, C.int(i), &p, &size, &id, &timeScale)
		stream.timeScale = int64(timeScale)
		b := C.GoBytes(p, size)
		switch id {
		case C.H264:
			stream.SetType(av.H264)
		case C.AAC:
			stream.SetType(av.AAC)
		}
		if err = stream.SetCodecData(b); err != nil {
			return
		}
		self.streams = append(self.streams, stream)
	}

	return
}

func (self *FFDemuxer) SeekToTime(time float64) (err error) {
	C.av_seek_frame(self.ctx.fmt_ctx, -1, C.int64_t(time*float64(C.AV_TIME_BASE)), 0)
	return
}

func (self *FFDemuxer) Time() float64 {
	return self.time
}

func (self *FFDemuxer) ReadPacket() (i int, pkt av.Packet, err error) {
	C.ffctx_read_frame(self.ctx)
	cpkt := self.ctx.pkt
	i = int(cpkt.stream_index)
	stream := self.streams[i]
	self.time = float64(cpkt.dts)/float64(stream.timeScale)
	pkt.Data = C.GoBytes(unsafe.Pointer(cpkt.data), cpkt.size)
	pkt.Duration = float64(cpkt.duration)/float64(stream.timeScale)
	pkt.CompositionTime = float64(cpkt.pts-cpkt.dts)/float64(stream.timeScale)
	pkt.IsKeyFrame = cpkt.flags&C.AV_PKT_FLAG_KEY!=0
	C.ffctx_free_packet(self.ctx)
	return
}

func init() {
	C.av_register_all()
}

